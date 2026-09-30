package catalog

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"kinodom/internal/meta"
	"kinodom/internal/source"
)

// Найденное поиском сразу встаёт в срочную догрузку (замечание № 10 этапа 11b): первые 20 на трекер,
// в порядке выдачи трекера; уже догруженные — нет; форум на паузе — никто.
func TestSearchEnqueuesFoundForDetails(t *testing.T) {
	rutor := newFake("rutor")
	for i := range 25 {
		rutor.search = append(rutor.search, rel("rutor", fmt.Sprint(100+i), fmt.Sprintf("Найдено %d (2021) WEB-DL", i), 100-i, 1, fmt.Sprintf("h%02d", i)))
	}
	c, _ := newCatalog(t, openDB(t), nil, rutor)
	st := waitSearch(t, c, "найдено")
	if len(st.Results) != 25 {
		t.Fatalf("найдено %d", len(st.Results))
	}
	c.mu.Lock()
	urgent := slices.Clone(c.found["rutor"])
	c.mu.Unlock()
	if len(urgent) != searchToEnrich {
		t.Fatalf("в срочной догрузке %d, нужно %d", len(urgent), searchToEnrich)
	}
	byID, err := c.st.rowsByID(ctx, urgent)
	if err != nil {
		t.Fatal(err)
	}
	for k, id := range urgent {
		if want := fmt.Sprint(100 + k); byID[id].TopicID != want {
			t.Fatalf("срочная %d: тема %s, нужна %s (порядок выдачи)", k, byID[id].TopicID, want)
		}
	}

	paused := newFake("rutor")
	paused.search = rutor.search[:3]
	c2, _ := newCatalog(t, openDB(t), nil, paused)
	c2.pauseForum("rutor", c2.now().Add(time.Hour))
	waitSearch(t, c2, "найдено")
	c2.mu.Lock()
	n := len(c2.found["rutor"])
	c2.mu.Unlock()
	if n != 0 {
		t.Fatalf("форум на паузе, а в срочной догрузке %d", n)
	}
}

// Найденное поиском догружается без .torrent (найдено вживую, 11b-А): .torrent идёт через тот же
// ограничитель «1 запрос в секунду» и вдвое замедлял постеры поиска; открыли раздачу — .torrent сразу,
// и экран раздачи ждёт его как часть догрузки (без него сериал показался бы без серий).
func TestFoundEnrichedWithoutTorrentUntilOpened(t *testing.T) {
	rutor := newFake("rutor")
	found := rel("rutor", "77", "Сериал [S01] (2026) WEB-DL", 5, 1<<30, "77aa")
	rutor.details["77"] = source.Details{Release: found, Description: "Описание"}
	rutor.torrents["77"] = []byte("d4:infoe")
	c, _ := newCatalog(t, openDB(t), nil, rutor)
	ids, err := c.st.saveFound(ctx, []source.Release{found}, c.now())
	if err != nil {
		t.Fatal(err)
	}
	c.enqueueFound(ctx, "rutor", ids)
	if did, err := c.enrichStep(ctx, "rutor"); !did || err != nil {
		t.Fatal(did, err)
	}
	if rutor.Calls("details") != 1 || rutor.Calls("torrent") != 0 {
		t.Fatalf("найденное: страниц %d, .torrent %d — .torrent не нужен до открытия", rutor.Calls("details"), rutor.Calls("torrent"))
	}
	r, err := c.Release(ctx, ids[0])
	if err != nil || !r.DetailsPending {
		t.Fatalf("открыли без .torrent — экран должен ждать: pending=%v, %v", r.DetailsPending, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		r, _ = c.Release(ctx, ids[0])
		if !r.DetailsPending && r.Torrent != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf(".torrent открытой раздачи не пришёл: pending=%v, %d байт", r.DetailsPending, len(r.Torrent))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Фоновый шаг ждёт медленный .torrent Rutor (d.rutor.info отдаёт его 4–10 с, бывает и 20): найденное
// поиском или открытое в пульте его прерывает — страница фоновой раздачи уже есть, .torrent докачается
// повтором или при открытии (найдено вживую, 11b-А: постеры поиска стояли до 20 с).
func TestUrgentWorkInterruptsBackgroundTorrent(t *testing.T) {
	rutor := newFake("rutor")
	rutor.top["12"] = []source.Release{rel("rutor", "1", "Фон (2020) WEB-DL", 9, 1, "h1")}
	rutor.details["1"] = source.Details{Release: source.Release{Title: "Фон (2020) WEB-DL"}, Description: "Фон"}
	rutor.torrents["1"] = []byte("d4:infoe")
	rutor.torrentBlock = make(chan struct{})
	t.Cleanup(func() { close(rutor.torrentBlock) })
	found := rel("rutor", "2", "Найдено (2026) WEB-DL", 5, 1, "h2")
	rutor.details["2"] = source.Details{Release: found, Description: "Найдено"}
	c, _ := newCatalog(t, openDB(t), nil, rutor)
	refresh(t, c, true)
	done := make(chan error, 1)
	go func() { _, err := c.enrichStep(ctx, "rutor"); done <- err }()
	for deadline := time.Now().Add(5 * time.Second); rutor.Calls("torrent") == 0; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("фоновый шаг не дошёл до .torrent")
		}
	}
	ids, err := c.st.saveFound(ctx, []source.Release{found}, c.now())
	if err != nil {
		t.Fatal(err)
	}
	c.enqueueFound(ctx, "rutor", ids)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("найденное ждёт, пока фоновый шаг дождётся .torrent")
	}
	bg := releaseID(t, c.db, "rutor", "1")
	r, err := c.Release(ctx, bg)
	if err != nil || r.Description != "Фон" {
		t.Fatalf("страница фоновой раздачи не записалась: %q, %v", r.Description, err)
	}
	if !c.due("torrent", bg, c.now()) {
		t.Fatal("прерванный .torrent — не сбой: при открытии его качают сразу")
	}
}

// .torrent открытой раздачи не скачался — экран раздачи не ждёт его до следующего повтора: открывается
// по magnet.
func TestOpenedTorrentFailedStopsWaiting(t *testing.T) {
	rutor := newFake("rutor")
	found := rel("rutor", "78", "Фильм (2026) WEB-DL", 5, 1<<30, "78aa")
	rutor.details["78"] = source.Details{Release: found, Description: "Описание"}
	c, _ := newCatalog(t, openDB(t), nil, rutor)
	ids, err := c.st.saveFound(ctx, []source.Release{found}, c.now())
	if err != nil {
		t.Fatal(err)
	}
	c.enqueueFound(ctx, "rutor", ids)
	if did, err := c.enrichStep(ctx, "rutor"); !did || err != nil {
		t.Fatal(did, err)
	}
	if r, _ := c.Release(ctx, ids[0]); !r.DetailsPending {
		t.Fatal("первое открытие без .torrent — ждём его")
	}
	c.posterWG.Wait()
	r, err := c.Release(ctx, ids[0])
	if err != nil || r.DetailsPending || rutor.Calls("torrent") != 1 {
		t.Fatalf(".torrent не скачался: pending=%v, попыток %d — экран не должен ждать, повтор — по паузе", r.DetailsPending, rutor.Calls("torrent"))
	}
}

// Срочная раздача не ждёт чужой постер с медленного хостинга (хвост Х8): постер качается вне шага
// догрузки страницы.
func TestUrgentNotBlockedBySlowPoster(t *testing.T) {
	pic := pngBytes(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var once atomic.Bool
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if once.CompareAndSwap(false, true) {
			close(entered)
		}
		<-release
		w.Write(pic)
	}))
	t.Cleanup(host.Close)
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	im, err := meta.NewImages(meta.ImagesOptions{Dir: t.TempDir(), Rate: 1000, AllowPrivate: true})
	if err != nil {
		t.Fatal(err)
	}
	rutor := newFake("rutor")
	a := rel("rutor", "1", "А (2020) WEB-DL", 5, 1, "aa")
	b := rel("rutor", "2", "Б (2020) WEB-DL", 5, 1, "bb")
	rutor.details["1"] = source.Details{Release: a, PosterURL: host.URL + "/a.jpg"}
	rutor.details["2"] = source.Details{Release: b, PosterURL: host.URL + "/b.jpg"}
	c, _ := newCatalog(t, openDB(t), func(o *Options) { o.Images = im }, rutor)
	ids, err := c.st.saveFound(ctx, []source.Release{a, b}, c.now())
	if err != nil {
		t.Fatal(err)
	}
	c.enrichSoon("rutor", ids[0])
	c.enrichSoon("rutor", ids[1])
	stepDone := make(chan error, 2)
	go func() {
		for range 2 {
			_, err := c.enrichStep(ctx, "rutor")
			stepDone <- err
		}
	}()
	<-entered // постер первой качается и висит
	for range 2 {
		select {
		case err := <-stepDone:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("шаг догрузки ждёт постер с медленного хостинга")
		}
	}
	if r, _ := c.Release(ctx, ids[1]); r.DetailsPending {
		t.Fatal("вторая срочная раздача не догружена, пока висит постер первой")
	}
	close(release)
	c.posterWG.Wait()
	if r, _ := c.Release(ctx, ids[0]); r.ImageKey == "" {
		t.Fatal("постер не записался, когда хостинг ответил")
	}
}

// Заглушка хостинга (хвост Х6): одна картинка у трёх раздач с разных адресов — это заглушка; раздачи
// её теряют и получают постер Кинопоиска по номеру.
func TestStubPosterReplacedByKinopoisk(t *testing.T) {
	stub, kp := pngBytes(t), append(pngBytes(t), 1)
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/kp/301.jpg" {
			w.Write(kp)
			return
		}
		w.Write(stub)
	}))
	t.Cleanup(host.Close)
	im, err := meta.NewImages(meta.ImagesOptions{Dir: t.TempDir(), Rate: 1000, AllowPrivate: true, StubSources: meta.PosterStubSources})
	if err != nil {
		t.Fatal(err)
	}
	rutor := newFake("rutor")
	for i := range 3 {
		id := fmt.Sprint(i + 1)
		rutor.top["12"] = append(rutor.top["12"], rel("rutor", id, "Ф"+id+" (2020) WEB-DL", 9-i, 1, "h"+id))
		rutor.details[id] = source.Details{Release: source.Release{Title: "Ф" + id + " (2020) WEB-DL"}, PosterURL: host.URL + "/" + id + ".jpg", KinopoiskID: "301"}
	}
	c, clk := newCatalog(t, openDB(t), func(o *Options) {
		o.Images = im
		o.KinopoiskPoster = func(id int) string { return fmt.Sprintf("%s/kp/%d.jpg", host.URL, id) }
	}, rutor)
	refresh(t, c, true)
	enrichAll(t, c, "rutor")
	clk.add(assetRetry[0] + time.Second)
	if err := c.fixPosters(ctx); err != nil {
		t.Fatal(err)
	}
	for _, e := range list(t, c, ListOptions{}) {
		if e.ImageKey != meta.ImageKey(host.URL+"/kp/301.jpg") {
			t.Fatalf("раздача %s: картинка %q — заглушка не заменена постером Кинопоиска", e.TopicID, e.ImageKey)
		}
	}
}

// Постер страницы и .torrent Rutor, не скачавшиеся из-за сбоя, повторяются с паузой (хвост Х7):
// раньше паузы — нет, после — да.
func TestPosterAndTorrentRetried(t *testing.T) {
	pic := pngBytes(t)
	var up atomic.Bool
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !up.Load() {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.Write(pic)
	}))
	t.Cleanup(host.Close)
	im, err := meta.NewImages(meta.ImagesOptions{Dir: t.TempDir(), Rate: 1000, AllowPrivate: true})
	if err != nil {
		t.Fatal(err)
	}
	rutor := newFake("rutor")
	rutor.top["12"] = []source.Release{rel("rutor", "1", "А (2020) WEB-DL", 9, 1, "a")}
	rutor.details["1"] = source.Details{Release: source.Release{Title: "А (2020) WEB-DL"}, PosterURL: host.URL + "/p.png"}
	c, clk := newCatalog(t, openDB(t), func(o *Options) { o.Images = im }, rutor)
	refresh(t, c, true)
	enrichAll(t, c, "rutor")
	es := list(t, c, ListOptions{})
	// Раздачу читаем из базы, а не «открываем»: открытие без картинки повторяет постер сразу.
	torrentOf := func(id int64) []byte {
		t.Helper()
		_, _, _, b, _, err := c.st.release(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	if es[0].ImageKey != "" || torrentOf(es[0].ID) != nil {
		t.Fatalf("при сбое: картинка %q, .torrent %d байт", es[0].ImageKey, len(torrentOf(es[0].ID)))
	}
	up.Store(true)
	rutor.set(func() { rutor.torrents["1"] = []byte("d4:infoe") })
	if err := c.fixPosters(ctx); err != nil {
		t.Fatal(err)
	}
	if es = list(t, c, ListOptions{}); es[0].ImageKey != "" {
		t.Fatal("повтор раньше паузы")
	}
	clk.add(assetRetry[0] + time.Second)
	if err := c.fixPosters(ctx); err != nil {
		t.Fatal(err)
	}
	es = list(t, c, ListOptions{})
	if es[0].ImageKey != meta.ImageKey(host.URL+"/p.png") || torrentOf(es[0].ID) == nil {
		t.Fatalf("после паузы: картинка %q, .torrent %d байт", es[0].ImageKey, len(torrentOf(es[0].ID)))
	}
}
