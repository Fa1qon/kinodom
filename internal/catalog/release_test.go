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
	rutor := newFake("rutor")
	rutor.top["12"] = many("rutor", 3)
	found := rel("rutor", "77", "Найдено / Found (2021) WEB-DL 1080p", 5, 1<<30, "77aa")
	rutor.details["77"] = source.Details{Release: found, Description: "Описание раздачи", Magnet: "magnet:?xt=urn:btih:77aa"}
	c, _ := newCatalog(t, openDB(t), nil, rutor)
	refresh(t, c, false)
	ids, err := c.st.saveFound(ctx, []source.Release{found}, c.now())
	if err != nil {
		t.Fatal(err)
	}
	r, err := c.Release(ctx, ids[0])
	if err != nil || !r.DetailsPending || r.TrackerURL != "https://rutor.example/topic/77" || r.Title != found.Title {
		t.Fatalf("до догрузки: %+v, %v", r, err)
	}
	if did, err := c.enrichStep(ctx, "rutor"); !did || err != nil {
		t.Fatal(did, err)
	}
	if rutor.Calls("details") != 1 || rutor.Calls("details:77") != 1 {
		t.Fatalf("первой догружена не открытая раздача: страниц %d, её — %d", rutor.Calls("details"), rutor.Calls("details:77"))
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

// Хостинг постера мёртв, и Кинопоиск в момент догрузки тоже не отдал постер: постер Кинопоиска
// догружается позже; открытие раздачи без картинки будит догрузку сразу (хвост 5c).
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
	im, err := meta.NewImages(meta.ImagesOptions{Dir: t.TempDir(), Rate: 1000})
	if err != nil {
		t.Fatal(err)
	}
	db := openDB(t)
	rutor := newFake("rutor")
	rutor.top["12"] = []source.Release{rel("rutor", "1", "А (2020) WEB-DL", 9, 1, "a")}
	rutor.details["1"] = source.Details{Release: source.Release{Title: "А (2020) WEB-DL"}, PosterURL: host.URL + "/dead.jpg", KinopoiskID: "301"}
	c, _ := newCatalog(t, db, func(o *Options) {
		o.Images = im
		o.KinopoiskPoster = func(id int) string { return host.URL + "/kp/301.jpg" }
		o.Ratings = meta.NewRatings(meta.RatingsOptions{KP: meta.NewKinopoisk(meta.KinopoiskOptions{}), DB: db})
	}, rutor)
	refresh(t, c, true)
	enrichAll(t, c, "rutor")
	es := list(t, c, ListOptions{})
	if es[0].ImageKey != "" {
		t.Fatalf("картинка при мёртвых хостингах: %q", es[0].ImageKey)
	}
	kpUp.Store(true)
	if _, err := c.Release(ctx, es[0].ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-c.postersWake:
	default:
		t.Fatal("открытие раздачи без картинки не разбудило догрузку постеров")
	}
	if err := c.fixPosters(ctx); err != nil {
		t.Fatal(err)
	}
	if es := list(t, c, ListOptions{}); es[0].ImageKey != meta.ImageKey(host.URL+"/kp/301.jpg") {
		t.Fatalf("постер Кинопоиска не догрузился: %q", es[0].ImageKey)
	}
}

// magnetSource — трекер, который собирает magnet со своими трекерами (как Rutracker).
type magnetSource struct{ *fakeSource }

func (m magnetSource) Magnet(ih string) string {
	return "magnet:?xt=urn:btih:" + ih + "&tr=http://ann.example"
}

// Страницы раздачи ещё нет, а infohash известен из топа — «Скачать» не ждёт догрузки: magnet
// собирает источник со своими трекерами, а у источника без них — голый magnet (DHT).
func TestReleaseMagnetBeforeDetails(t *testing.T) {
	rt, rutor := newFake("rutracker"), newFake("rutor")
	rt.top["2110"] = []source.Release{rel("rutracker", "7", "", 30, 1, "aa77")}
	rutor.top["12"] = []source.Release{rel("rutor", "8", "Б (2020) WEB-DL", 20, 1, "bb88")}
	c := New(Options{DB: openDB(t), Sources: []source.Source{magnetSource{rt}, rutor},
		Sections: []Section{{"rutracker", "2110", false}, {"rutor", "12", false}}})
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
