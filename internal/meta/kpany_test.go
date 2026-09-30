package meta

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

func newKPAny(t *testing.T, w *fakeKPWeb, f *fakeKP, key string, types func(int) string) *KPAny {
	t.Helper()
	clk := &clock{t: time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local)}
	a := &KPAny{Web: NewKPWeb(KPWebOptions{GraphQL: w.URL + "/graphql/", Site: w.URL, Every: time.Millisecond, Now: clk.now}),
		Key: newKP(f, key)}
	if types != nil {
		a.Types = func(_ context.Context, id int) (string, error) { return types(id), nil }
	}
	return a
}

// Медиатека ищет без ключа — поиском сайта, в очереди после каталога (спека 11b, 5.6).
func TestKPAnyKeylessFirst(t *testing.T) {
	w, f := newFakeKPWeb(t), newFakeKP(t)
	w.suggest["Законник"] = "kpweb-suggest-zakonnik.json"
	a := newKPAny(t, w, f, testKey, nil)
	fs, err := a.Search(ctx, "Законник", 2023)
	if err != nil || len(fs) == 0 || fs[0].ID != 5325705 || fs[0].Type != "TV_SERIES" {
		t.Fatalf("поиск: %+v, %v", fs, err)
	}
	if f.Hits("search") != 0 {
		t.Fatal("ключ тратится, хотя сайт ответил")
	}
}

// Сайт на паузе — ключ, если задан; без ключа — ErrKPBlocked (медиатека ждёт).
func TestKPAnyFallsBackToKey(t *testing.T) {
	w, f := newFakeKPWeb(t), newFakeKP(t)
	w.status = http.StatusForbidden
	a := newKPAny(t, w, f, testKey, nil)
	fs, err := a.Search(ctx, "The Matrix", 1999)
	if err != nil || len(fs) == 0 || fs[0].ID != 301 {
		t.Fatalf("поиск ключом: %+v, %v", fs, err)
	}
	b := newKPAny(t, w, newFakeKP(t), "", nil)
	if _, err := b.Search(ctx, "The Matrix", 1999); !errors.Is(err, ErrKPBlocked) {
		t.Fatalf("без ключа: %v", err)
	}
}

// Карточка сериала: вид из базы — сразу запрос сериала; вид неизвестен — фильм, «нет такого» — сериал.
func TestKPAnyDetailsSeries(t *testing.T) {
	w, f := newFakeKPWeb(t), newFakeKP(t)
	w.series[7036356] = "kpweb-series-7036356.json"
	a := newKPAny(t, w, f, "", func(int) string { return "TV_SERIES" })
	d, err := a.Details(ctx, 7036356)
	if err != nil || d.NameRu != "Холод" || len(w.Calls()) != 1 {
		t.Fatalf("вид известен: %+v, %v, запросов %d", d.Film, err, len(w.Calls()))
	}
	w2 := newFakeKPWeb(t)
	w2.series[7036356] = "kpweb-series-7036356.json"
	b := newKPAny(t, w2, f, "", func(int) string { return "" })
	d, err = b.Details(ctx, 7036356)
	if err != nil || d.Type != "TV_SERIES" || len(w2.Calls()) != 2 {
		t.Fatalf("вид неизвестен: %+v, %v, запросов %d", d.Film, err, len(w2.Calls()))
	}
}
