package catalog

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"kinodom/internal/source"
)

func historyQueries(t *testing.T, c *Catalog) []string {
	t.Helper()
	h, err := c.History(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, it := range h {
		out = append(out, it.Query)
	}
	return out
}

// История поиска: одинаковые запросы без учёта регистра и пробелов — одна запись, последняя —
// первой; помнятся 20 последних; запрос убирается по одному и вся история — целиком.
func TestSearchHistory(t *testing.T) {
	c, clk := newCatalog(t, openDB(t), nil, newFake("rutor"))
	for _, q := range []string{"космос", "луна", "  Космос  "} {
		clk.add(time.Second)
		if err := c.rememberQuery(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	if got := fmt.Sprint(historyQueries(t, c)); got != "[Космос луна]" {
		t.Fatalf("история: %s", got)
	}
	for i := range 25 {
		clk.add(time.Second)
		c.rememberQuery(ctx, fmt.Sprintf("запрос %d", i))
	}
	h := historyQueries(t, c)
	if len(h) != 20 || h[0] != "запрос 24" || h[19] != "запрос 5" {
		t.Fatalf("20 последних: %v", h)
	}
	if err := c.ForgetQuery(ctx, "ЗАПРОС 24"); err != nil {
		t.Fatal(err)
	}
	if h := historyQueries(t, c); len(h) != 19 || h[0] != "запрос 23" {
		t.Fatalf("убрали один: %v", h)
	}
	c.ForgetQuery(ctx, "")
	if h := historyQueries(t, c); len(h) != 0 {
		t.Fatalf("очистили: %v", h)
	}
}

// Маршрут поиска: первый запрос — в историю, опросы того же поиска (poll=1) — нет; пустой запрос —
// 400; история убирается запросом DELETE.
func TestSearchRoutesAndHistory(t *testing.T) {
	rutor := newFake("rutor")
	rutor.search = []source.Release{rel("rutor", "5", "Космос (1980) BDRip", 40, 1<<30, "k")}
	c, _ := newCatalog(t, openDB(t), nil, rutor)
	mux := http.NewServeMux()
	c.Register(muxRouter{mux})
	var v SearchView
	for deadline := time.Now().Add(5 * time.Second); !v.Complete; time.Sleep(20 * time.Millisecond) {
		getJSON(t, mux, "/api/v1/search?q=космос&poll=1", &v)
		if time.Now().After(deadline) {
			t.Fatalf("поиск не закончился: %+v", v)
		}
	}
	if len(v.Results) != 1 || v.Results[0].Name != "Космос" || v.Trackers["rutor"] != SearchOK {
		t.Fatalf("поиск: %+v", v)
	}
	if h := historyQueries(t, c); len(h) != 0 {
		t.Fatalf("опрос попал в историю: %v", h)
	}
	getJSON(t, mux, "/api/v1/search?q=космос", &v)
	var h []HistoryItem
	if code := getJSON(t, mux, "/api/v1/search/history", &h); code != 200 || len(h) != 1 || h[0].Query != "космос" {
		t.Fatalf("история: %d %+v", code, h)
	}
	if code := getJSON(t, mux, "/api/v1/search?q=%20", nil); code != http.StatusBadRequest {
		t.Fatalf("пустой запрос: %d", code)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("DELETE", "/api/v1/search/history?q=КОСМОС", nil))
	if rec.Code != http.StatusNoContent || len(historyQueries(t, c)) != 0 {
		t.Fatalf("убрать запрос: %d %v", rec.Code, historyQueries(t, c))
	}
}

// Каталог остановили посреди поиска — у трекера понятный текст, а не «context canceled» (ревью 5c).
func TestSearchAfterStopSaysStopped(t *testing.T) {
	rutor := newFake("rutor")
	rutor.searchBlock = make(chan struct{})
	c, _ := newCatalog(t, openDB(t), nil, rutor)
	runCtx, stop := context.WithCancel(ctx)
	c.mu.Lock()
	c.runCtx = runCtx
	c.mu.Unlock()
	if _, err := c.Search(ctx, "космос"); err != nil {
		t.Fatal(err)
	}
	stop()
	var st SearchState
	for deadline := time.Now().Add(5 * time.Second); !st.Complete; time.Sleep(20 * time.Millisecond) {
		st, _ = c.Search(ctx, "космос")
		if time.Now().After(deadline) {
			t.Fatal("поиск не закончился после остановки")
		}
	}
	if st.Trackers["rutor"] != "каталог остановлен" {
		t.Fatalf("текст: %q", st.Trackers["rutor"])
	}
}
