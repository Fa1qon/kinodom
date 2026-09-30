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
	t1 := newFake("t1")
	for i := range 25 {
		t1.search = append(t1.search, rel("t1", fmt.Sprint(100+i), fmt.Sprintf("Найдено %d (2021) WEB-DL", i), 100-i, 1, fmt.Sprintf("h%02d", i)))
	}
	c, _ := newCatalog(t, openDB(t), nil, t1)
	st := waitSearch(t, c, "найдено")
	if len(st.Results) != 25 {
		t.Fatalf("найдено %d", len(st.Results))
	}
	c.mu.Lock()
	urgent := slices.Clone(c.urgent["t1"])
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

	paused := newFake("t1")
	paused.search = t1.search[:3]
	c2, _ := newCatalog(t, openDB(t), nil, paused)
	c2.pauseForum("t1", c2.now().Add(time.Hour))
	waitSearch(t, c2, "найдено")
	c2.mu.Lock()
	n := len(c2.urgent["t1"])
	c2.mu.Unlock()
	if n != 0 {
		t.Fatalf("форум на паузе, а в срочной догрузке %d", n)
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
	t1 := newFake("t1")
	a := rel("t1", "1", "А (2020) WEB-DL", 5, 1, "aa")
	b := rel("t1", "2", "Б (2020) WEB-DL", 5, 1, "bb")
	t1.details["1"] = source.Details{Release: a, PosterURL: host.URL + "/a.jpg"}
	t1.details["2"] = source.Details{Release: b, PosterURL: host.URL + "/b.jpg"}
	c, _ := newCatalog(t, openDB(t), func(o *Options) { o.Images = im }, t1)
	ids, err := c.st.saveFound(ctx, []source.Release{a, b}, c.now())
	if err != nil {
		t.Fatal(err)
	}
	c.enrichSoon("t1", ids[0])
	c.enrichSoon("t1", ids[1])
	stepDone := make(chan error, 2)
	go func() {
		for range 2 {
			_, err := c.enrichStep(ctx, "t1")
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
	im, err := meta.NewImages(meta.ImagesOptions{Dir: t.TempDir(), Rate: 1000, AllowPrivate: true})
	if err != nil {
		t.Fatal(err)
	}
	t1 := newFake("t1")
	for i := range 3 {
		id := fmt.Sprint(i + 1)
		t1.top["12"] = append(t1.top["12"], rel("t1", id, "Ф"+id+" (2020) WEB-DL", 9-i, 1, "h"+id))
		t1.details[id] = source.Details{Release: source.Release{Title: "Ф" + id + " (2020) WEB-DL"}, PosterURL: host.URL + "/" + id + ".jpg", KinopoiskID: "301"}
	}
	c, clk := newCatalog(t, openDB(t), func(o *Options) {
		o.Images = im
		o.KinopoiskPoster = func(id int) string { return fmt.Sprintf("%s/kp/%d.jpg", host.URL, id) }
	}, t1)
	refresh(t, c, true)
	enrichAll(t, c, "t1")
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

// Постер страницы и .torrent t1, не скачавшиеся из-за сбоя, повторяются с паузой (хвост Х7):
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
	t1 := newFake("t1")
	t1.top["12"] = []source.Release{rel("t1", "1", "А (2020) WEB-DL", 9, 1, "a")}
	t1.details["1"] = source.Details{Release: source.Release{Title: "А (2020) WEB-DL"}, PosterURL: host.URL + "/p.png"}
	c, clk := newCatalog(t, openDB(t), func(o *Options) { o.Images = im }, t1)
	refresh(t, c, true)
	enrichAll(t, c, "t1")
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
	t1.set(func() { t1.torrents["1"] = []byte("d4:infoe") })
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
