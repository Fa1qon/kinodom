package catalog

import (
	"net/http"
	"testing"
	"time"

	"kinodom/internal/source"
)

func TestNewReleasesSeparatesKindsAndKeepsLatestWork(t *testing.T) {
	t1 := newFake("t1")
	t1.top["12"] = []source.Release{
		rel("t1", "1", "Новый фильм (2026) WEB-DL 1080p", 10, 1<<30, "film-new"),
		rel("t1", "2", "Новый фильм (2026) Blu-ray", 20, 2<<30, "film-old-version"),
		rel("t1", "3", "Новый сериал [S01] (2026) WEB-DL 1080p", 30, 3<<30, "series-new"),
		rel("t1", "4", "Старый фильм (2025) WEB-DL 1080p", 40, 4<<30, "film-old"),
		rel("t1", "5", "Наш сериал [S01] (2026) WEB-DL 1080p", 50, 5<<30, "series-our"),
		rel("t1", "6", "Наш сериал без номера (2026) WEB-DL 1080p", 45, 6<<30, "series-our-no-marker"),
	}
	c, _ := newCatalog(t, openDB(t), nil, t1)
	refresh(t, c, false)

	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	if _, err := c.st.db.W.Exec(`UPDATE releases SET added_at = CASE topic_id
		WHEN '1' THEN ? WHEN '2' THEN ? WHEN '3' THEN ? WHEN '4' THEN ? WHEN '5' THEN ? WHEN '6' THEN ? ELSE added_at END`,
		now.Add(-time.Hour).UnixMilli(), now.Add(-2*time.Hour).UnixMilli(), now.Add(-30*time.Minute).UnixMilli(), now.Add(-24*time.Hour).UnixMilli(), now.Add(-45*time.Minute).UnixMilli(), now.Add(-15*time.Minute).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.st.db.W.Exec(`UPDATE releases SET category_id = '16' WHERE topic_id IN ('5', '6')`); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	c.Register(muxRouter{mux})
	var filmsView struct {
		Entries []EntryView `json:"entries"`
	}
	if code := getJSON(t, mux, "/api/v1/catalog/new?group=foreign-film&limit=10", &filmsView); code != http.StatusOK {
		t.Fatalf("films status: %d", code)
	}
	if len(filmsView.Entries) != 2 || filmsView.Entries[0].Name != "Новый фильм" || filmsView.Entries[1].Name != "Старый фильм" {
		t.Fatalf("films: %+v", filmsView.Entries)
	}

	var seriesView struct {
		Entries []EntryView `json:"entries"`
	}
	if code := getJSON(t, mux, "/api/v1/catalog/new?group=foreign-series&limit=10", &seriesView); code != http.StatusOK {
		t.Fatalf("series status: %d", code)
	}
	if len(seriesView.Entries) != 1 || seriesView.Entries[0].Name != "Новый сериал" || seriesView.Entries[0].Season == "" {
		t.Fatalf("series: %+v", seriesView.Entries)
	}
	var ourSeries struct {
		Entries []EntryView `json:"entries"`
	}
	if code := getJSON(t, mux, "/api/v1/catalog/new?group=our-series&limit=10", &ourSeries); code != http.StatusOK || len(ourSeries.Entries) != 2 || ourSeries.Entries[0].Name != "Наш сериал без номера" || ourSeries.Entries[1].Name != "Наш сериал" {
		t.Fatalf("our series: %d %+v", code, ourSeries.Entries)
	}
}

func TestNewReleasesRejectsUnknownKind(t *testing.T) {
	t1 := newFake("t1")
	c, _ := newCatalog(t, openDB(t), nil, t1)
	mux := http.NewServeMux()
	c.Register(muxRouter{mux})
	if code := getJSON(t, mux, "/api/v1/catalog/new?group=other", nil); code != http.StatusBadRequest {
		t.Fatalf("unknown kind status: %d", code)
	}
}
