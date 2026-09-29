package catalog

import (
	"net/http"
	"testing"

	"kinodom/internal/source"
)

// Каталог трекера — страницами по 24 раздачи раздела, по раздающим; без раздела — первый раздел
// трекера с раздачами; страница вне диапазона — пусто (спека этапа 7, раздел 5.4).
func TestListRoutePagesBySection(t *testing.T) {
	rutor, rt := newFake("rutor"), newFake("rutracker")
	rutor.top["12"] = many("rutor", 30)
	rutor.top["5"] = many("rutor", 2)
	rt.top["2110"] = many("rutracker", 3)
	c, clk := newCatalog(t, openDB(t), func(o *Options) {
		o.Sections = []Section{{"rutor", "12", false}, {"rutor", "5", false}, {"rutracker", "2110", false}}
	}, rutor, rt)
	refresh(t, c, false)
	mux := http.NewServeMux()
	c.Register(muxRouter{mux})
	var v ListView
	if code := getJSON(t, mux, "/api/v1/catalog?tracker=rutor", &v); code != 200 || v.Section != "12" || v.Page != 1 ||
		v.Pages != 2 || len(v.Entries) != 24 || v.Entries[0].Seeders != 100 || v.UpdatedAt == nil || !v.UpdatedAt.Equal(clk.now()) {
		t.Fatalf("первая страница: %d %+v", code, v)
	}
	if e := v.Entries[0]; e.Name != "Фильм 0" || e.Year != 2020 || e.Quality != "WEB-DL" || e.Tracker != "rutor" {
		t.Fatalf("карточка: %+v", e)
	}
	getJSON(t, mux, "/api/v1/catalog?tracker=rutor&section=12&page=2", &v)
	if len(v.Entries) != 6 || v.Entries[0].Seeders != 76 {
		t.Fatalf("вторая страница: %+v", v)
	}
	getJSON(t, mux, "/api/v1/catalog?tracker=rutor&section=12&page=9", &v)
	if len(v.Entries) != 0 || v.Pages != 2 {
		t.Fatalf("за последней страницей: %+v", v)
	}
	getJSON(t, mux, "/api/v1/catalog?tracker=rutracker", &v)
	if v.Section != "2110" || len(v.Entries) != 3 || v.Entries[0].Tracker != "rutracker" {
		t.Fatalf("Rutracker: %+v", v)
	}
	if code := getJSON(t, mux, "/api/v1/catalog?tracker=kinozal", nil); code != http.StatusNotFound {
		t.Fatalf("незнакомый трекер: %d", code)
	}
}

// Отрицательное смещение — с начала, а не panic (ревью 5c).
func TestListNegativeOffset(t *testing.T) {
	rutor := newFake("rutor")
	rutor.top["12"] = many("rutor", 3)
	c, _ := newCatalog(t, openDB(t), nil, rutor)
	refresh(t, c, false)
	if es := list(t, c, ListOptions{Tracker: "rutor", Offset: -5, Limit: 2}); len(es) != 2 || es[0].TopicID != "1" {
		t.Fatalf("%+v", es)
	}
}

// Одна раздача на двух трекерах (одинаковый infohash) — в каталоге каждого трекера: трекеры теперь
// показываются отдельно (спека этапа 7, раздел 3).
func TestSameInfohashCountsInEachTracker(t *testing.T) {
	rutor, rt := newFake("rutor"), newFake("rutracker")
	rutor.top["12"] = []source.Release{rel("rutor", "1", "Огненный лис (2024) WEB-DL", 50, 1<<30, "same")}
	rt.top["2110"] = []source.Release{rel("rutracker", "2", "", 80, 1<<30, "same")}
	c, _ := newCatalog(t, openDB(t), nil, rutor, rt)
	refresh(t, c, false)
	cats, err := c.Categories(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, cat := range cats {
		if cat.Count != 1 {
			t.Fatalf("раздел %s:%s: %d", cat.Tracker, cat.ID, cat.Count)
		}
	}
	if es := list(t, c, ListOptions{Tracker: "rutor", Category: "12"}); len(es) != 1 {
		t.Fatalf("Rutor без своей раздачи: %+v", es)
	}
}
