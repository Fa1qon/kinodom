package catalog

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"kinodom/internal/meta"
	"kinodom/internal/source"
)

// Раздачу нашли поиском и открыли: её страница догружается первой, не дожидаясь очереди каталога,
// а экран раздачи знает, что она ещё догружается (хвост 5c).
func TestOpenedReleaseIsEnrichedFirst(t *testing.T) {
	t1 := newFake("t1")
	t1.top["12"] = many("t1", 3)
	found := rel("t1", "77", "Найдено / Found (2021) WEB-DL 1080p", 5, 1<<30, "77aa")
	t1.details["77"] = source.Details{Release: found, Description: "Описание раздачи", Magnet: "magnet:?xt=urn:btih:77aa"}
	c, _ := newCatalog(t, openDB(t), nil, t1)
	refresh(t, c, false)
	ids, err := c.st.saveFound(ctx, []source.Release{found}, c.now())
	if err != nil {
		t.Fatal(err)
	}
	r, err := c.Release(ctx, ids[0])
	if err != nil || !r.DetailsPending || r.TrackerURL != "https://t1.example/topic/77" || r.Title != found.Title {
		t.Fatalf("до догрузки: %+v, %v", r, err)
	}
	if did, err := c.enrichStep(ctx, "t1"); !did || err != nil {
		t.Fatal(did, err)
	}
	if t1.Calls("details") != 1 || t1.Calls("details:77") != 1 {
		t.Fatalf("первой догружена не открытая раздача: страниц %d, её — %d", t1.Calls("details"), t1.Calls("details:77"))
	}
	r, err = c.Release(ctx, ids[0])
	v := r.View()
	if err != nil || r.DetailsPending || v.Description != "Описание раздачи" || v.Hash != "77aa" || v.Name != "Найдено" || v.Year != 2021 {
		t.Fatalf("после догрузки: %+v, %v", v, err)
	}
	if _, err := c.Release(ctx, 99999); !errors.Is(err, ErrNoRelease) {
		t.Fatalf("нет раздачи: %v", err)
	}
}

// Открытую раздачу не держит её постер с медленного хостинга, а «догружена» она только вместе со
// списком серий из .torrent t1: иначе пульт перестал бы опрашивать и показал сериал без серий
// (финальное ревью 7a).
func TestReleaseReadyBeforeItsPoster(t *testing.T) {
	pic := pngBytes(t)
	entered, release := make(chan struct{}), make(chan struct{})
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
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
	found := rel("t1", "77", "Сериал [S01] (2025) WEB-DL", 5, 1<<30, "77aa")
	t1.details["77"] = source.Details{Release: found, Description: "Описание", PosterURL: host.URL + "/p.jpg"}
	t1.torrents["77"] = []byte("d4:infoe")
	c, _ := newCatalog(t, openDB(t), func(o *Options) { o.Images = im }, t1)
	ids, err := c.st.saveFound(ctx, []source.Release{found}, c.now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Release(ctx, ids[0]); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := c.enrichStep(ctx, "t1"); done <- err }()
	<-entered
	r, err := c.Release(ctx, ids[0])
	if err != nil || r.DetailsPending || r.Torrent == nil || r.Description != "Описание" {
		t.Fatalf("пока качается постер: pending=%v, .torrent %d байт, описание %q, %v", r.DetailsPending, len(r.Torrent), r.Description, err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	c.posterWG.Wait()
	if r, _ = c.Release(ctx, ids[0]); r.ImageKey == "" {
		t.Fatal("постер не записался после догрузки")
	}
}

// Хостинг постера мёртв, и Кинопоиск в момент догрузки тоже не отдал постер: постер Кинопоиска
// догружается позже; открытие раздачи без картинки качает его сразу (хвост 5c; спека 11b, 14.2 — не через
// проход повторов).
func TestKinopoiskPosterIsFetchedLater(t *testing.T) {
	pic := pngBytes(t)
	var kpUp atomic.Bool
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/kp/301.jpg" && kpUp.Load() {
			w.Write(pic)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(host.Close)
	im, err := meta.NewImages(meta.ImagesOptions{Dir: t.TempDir(), Rate: 1000, AllowPrivate: true})
	if err != nil {
		t.Fatal(err)
	}
	db := openDB(t)
	t1 := newFake("t1")
	t1.top["12"] = []source.Release{rel("t1", "1", "А (2020) WEB-DL", 9, 1, "a")}
	t1.details["1"] = source.Details{Release: source.Release{Title: "А (2020) WEB-DL"}, PosterURL: host.URL + "/dead.jpg", KinopoiskID: "301"}
	c, _ := newCatalog(t, db, func(o *Options) {
		o.Images = im
		o.KinopoiskPoster = func(id int) string { return host.URL + "/kp/301.jpg" }
		o.Ratings = meta.NewRatings(meta.RatingsOptions{KP: meta.NewKinopoisk(meta.KinopoiskOptions{}), DB: db})
	}, t1)
	refresh(t, c, true)
	enrichAll(t, c, "t1")
	es := list(t, c, ListOptions{})
	if es[0].ImageKey != "" {
		t.Fatalf("картинка при мёртвых хостингах: %q", es[0].ImageKey)
	}
	kpUp.Store(true)
	if _, err := c.Release(ctx, es[0].ID); err != nil {
		t.Fatal(err)
	}
	c.posterWG.Wait()
	if es := list(t, c, ListOptions{}); es[0].ImageKey != meta.ImageKey(host.URL+"/kp/301.jpg") {
		t.Fatalf("постер Кинопоиска не догрузился: %q", es[0].ImageKey)
	}
}

// magnetSource — трекер, который собирает magnet со своими трекерами (как t2).
type magnetSource struct{ *fakeSource }

func (m magnetSource) Magnet(ih string) string {
	return "magnet:?xt=urn:btih:" + ih + "&tr=http://ann.example"
}

// Страницы раздачи ещё нет, а infohash известен из топа — «Скачать» не ждёт догрузки: magnet
// собирает источник со своими трекерами, а у источника без них — голый magnet (DHT).
func TestReleaseMagnetBeforeDetails(t *testing.T) {
	rt, t1 := newFake("t2"), newFake("t1")
	rt.top["2110"] = []source.Release{rel("t2", "7", "", 30, 1, "aa77")}
	t1.top["12"] = []source.Release{rel("t1", "8", "Б (2020) WEB-DL", 20, 1, "bb88")}
	c := New(Options{DB: openDB(t), Sources: []source.Source{magnetSource{rt}, t1},
		Sections: []Section{{"t2", "2110", false}, {"t1", "12", false}}})
	refresh(t, c, false)
	for _, e := range list(t, c, ListOptions{}) {
		r, err := c.Release(ctx, e.ID)
		if err != nil {
			t.Fatal(err)
		}
		want := map[string]string{"7": "magnet:?xt=urn:btih:aa77&tr=http://ann.example", "8": "magnet:?xt=urn:btih:bb88"}[e.TopicID]
		if r.Magnet != want {
			t.Fatalf("раздача %s: %q", e.TopicID, r.Magnet)
		}
	}
}

// ReleaseBrief — название и картинка без побочных действий Release: строку «Новых серий» опрашивает
// колокольчик раз в 15 с, догрузку и постер без паузы это не запускает (финальное ревью 11b-В).
func TestReleaseBriefHasNoSideEffects(t *testing.T) {
	t1 := newFake("t1")
	t1.top["12"] = []source.Release{rel("t1", "5", "Холод [01x01-06 из 08] (2026) WEB-DL 1080p", 9, 1, "aa05")}
	c, _ := newCatalog(t, openDB(t), nil, t1)
	refresh(t, c, false)
	es := list(t, c, ListOptions{})
	r, err := c.ReleaseBrief(ctx, es[0].ID)
	if err != nil || r.ID != es[0].ID || r.Title != "Холод [01x01-06 из 08] (2026) WEB-DL 1080p" || r.Quality != "WEB-DL 1080p" {
		t.Fatalf("кратко: %+v, %v", r, err)
	}
	c.mu.Lock()
	urgent, posters := len(c.urgent["t1"]), len(c.openPoster)
	c.mu.Unlock()
	if urgent != 0 || posters != 0 {
		t.Fatalf("побочные действия: срочная догрузка %d, постер открытой %d", urgent, posters)
	}
	if _, err := c.ReleaseBrief(ctx, 999); !errors.Is(err, ErrNoRelease) {
		t.Fatalf("нет раздачи: %v", err)
	}
}
