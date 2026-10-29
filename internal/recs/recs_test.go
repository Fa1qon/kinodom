package recs

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"encoding/json"

	"kinodom/internal/history"
	"kinodom/internal/store"
)

// testPack — маленький пакет модели: фильмы 1..n, связи и теги из аргументов.
func testPack(t *testing.T, films map[int]string, links [][3]float64, tags map[int]map[string]float64) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "recs-model.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range []string{
		`CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
		`CREATE TABLE films (kp_id INTEGER PRIMARY KEY, imdb_id TEXT, kind TEXT, title_ru TEXT, title_orig TEXT, year INTEGER, genres TEXT,
			countries TEXT, duration INTEGER, description TEXT, poster TEXT, rating_kp REAL, rating_imdb REAL, popularity REAL)`,
		`CREATE TABLE neighbors (kp_id INTEGER, other_kp INTEGER, weight REAL, PRIMARY KEY (kp_id, other_kp))`,
		`CREATE TABLE tags (kp_id INTEGER, tag TEXT, score REAL, PRIMARY KEY (kp_id, tag))`,
		`INSERT INTO meta VALUES ('version', '1')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	for id, title := range films {
		kind := "film"
		if strings.HasPrefix(title, "Сериал") {
			kind = "series"
		}
		db.Exec(`INSERT INTO films VALUES (?, '', ?, ?, ?, 2010, 'драма', '', 100, '', 'https://x/p.jpg', 7, 7, ?)`, id, kind, title, title, 1000-id)
	}
	for _, l := range links {
		db.Exec(`INSERT INTO neighbors VALUES (?, ?, ?)`, int(l[0]), int(l[1]), l[2])
	}
	for id, ts := range tags {
		for tag, v := range ts {
			db.Exec(`INSERT INTO tags VALUES (?, ?, ?)`, id, tag, v)
		}
	}
	return path
}

type fixture struct {
	ctx  context.Context
	s    *Service
	h    *history.Service
	now  *time.Time
	film map[string]int // hash → kp
}

func newFixture(t *testing.T, pack string) *fixture {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	now := time.Date(2026, 10, 29, 20, 0, 0, 0, time.UTC)
	fx := &fixture{ctx: ctx, now: &now, film: map[string]int{}}
	fx.h = history.New(db)
	fx.h.SetFilmOf(func(_ context.Context, hash string) int { return fx.film[hash] })
	fx.s = &Service{DB: db, Journal: fx.h, Now: func() time.Time { return *fx.now }}
	m, err := OpenModel(pack)
	if err != nil {
		t.Fatal(err)
	}
	fx.s.SetModel(m)
	t.Cleanup(func() { fx.s.SetModel(nil) })
	return fx
}

// watch — устройство смотрит фильм kp (раздача hash) подряд от from до to.
func (fx *fixture) watch(t *testing.T, device, hash string, kp int, from, to float64) {
	t.Helper()
	fx.film[hash] = kp
	const dur = 600.0
	for pos := from * dur; pos <= to*dur; pos += 10 {
		if err := fx.h.SetPosition(fx.ctx, device, hash, 0, pos, dur); err != nil {
			t.Fatal(err)
		}
		*fx.now = fx.now.Add(10 * time.Second)
	}
}

func titles(rs []Rec) []string {
	var out []string
	for _, r := range rs {
		out = append(out, r.Film.Title)
	}
	return out
}

var films = map[int]string{1: "Матрица", 2: "Начало", 3: "Довод", 4: "Сумерки", 5: "Новолуние", 6: "Паддингтон", 7: "Амели", 8: "Пила", 9: "Затмение"}

func TestJournalDrivesTaste(t *testing.T) {
	pack := testPack(t, films, [][3]float64{
		{1, 2, 0.3}, {1, 3, 0.2}, // «Матрица» → «Начало», «Довод»
		{4, 5, 0.4}, {4, 9, 0.3}, // «Сумерки» → «Новолуние», «Затмение»
		{6, 7, 0.3}, {8, 5, 0.1},
	}, nil)
	fx := newFixture(t, pack)
	const h1, h4, h8 = "aaaa000000000000000000000000000000000001", "aaaa000000000000000000000000000000000004", "aaaa000000000000000000000000000000000008"
	fx.watch(t, "tv", h1, 1, 0, 1)                          // «Матрицу» досмотрел подряд
	fx.watch(t, "tv", h4, 4, 0, 0.08)                       // «Сумерки» бросил на 8 %…
	fx.watch(t, "tv", h8, 8, 0, 0.1)                        // «Пилу» включил и
	fx.h.MarkEnded(fx.ctx, h8, 0, history.EndedDeleted, "") // …удалил сам
	*fx.now = fx.now.Add(4 * 24 * time.Hour)                // прошло 4 дня — «Сумерки» брошены, а не отложены

	sg, err := fx.s.signals(fx.ctx, "d:tv")
	if err != nil {
		t.Fatal(err)
	}
	p := sg.Build(*fx.now)
	if p[1] != xFinished || p[4] != xAbandoned || p[8] != xDeleted {
		t.Fatalf("профиль из журнала: %v", p)
	}
	rs, err := fx.s.Recommend(fx.ctx, "d:tv", Query{Limit: 4})
	if err != nil {
		t.Fatal(err)
	}
	got := titles(rs)
	if len(got) < 2 || !slices.Contains(got[:2], "Начало") || !slices.Contains(got[:2], "Довод") {
		t.Fatalf("похожее на досмотренное — первым: %v", got)
	}
	for _, x := range []string{"Матрица", "Сумерки", "Пила"} {
		if slices.Contains(got, x) {
			t.Fatalf("просмотренное, брошенное или удалённое не советуется: %v", got)
		}
	}
	if i, j := slices.Index(got, "Новолуние"), slices.Index(got, "Паддингтон"); i >= 0 && j >= 0 && i < j {
		t.Fatalf("похожее на брошенное и удалённое — ниже нейтрального: %v", got)
	}
	if rs[0].Because == nil || rs[0].Because[0] != 1 {
		t.Fatalf("причина — «Матрица»: %+v", rs[0])
	}
}

func TestJustStartedIsNotAbandoned(t *testing.T) {
	fx := newFixture(t, testPack(t, films, nil, nil))
	fx.watch(t, "tv", "aaaa000000000000000000000000000000000004", 4, 0, 0.08)
	*fx.now = fx.now.Add(time.Hour) // вечер ещё не кончился
	sg, _ := fx.s.signals(fx.ctx, "d:tv")
	if x, ok := sg.Build(*fx.now)[4]; ok {
		t.Fatalf("только начатое — не минус: %v", x)
	}
	// открыл на 20 секунд проверить озвучку — не «смотрел»: фильм можно советовать
	fx.watch(t, "tv", "aaaa000000000000000000000000000000000006", 6, 0, 0.01)
	*fx.now = fx.now.Add(10 * 24 * time.Hour)
	sg, _ = fx.s.signals(fx.ctx, "d:tv")
	if sg.Seen(6) {
		t.Fatal("проба озвучки — не просмотр")
	}
}

func TestMoodAndGroup(t *testing.T) {
	pack := testPack(t, films, [][3]float64{
		{1, 2, 0.4}, {1, 6, 0.2}, {1, 8, -0.4}, // любителю «Матрицы» «Пила» не зайдёт
		{4, 5, 0.4}, {4, 6, 0.2}, {4, 2, -0.3},
	}, map[int]map[string]float64{6: {"cozy": 0.9}, 7: {"cozy": 0.8}, 8: {"scary": 0.95}})
	fx := newFixture(t, pack)
	fx.s.Rate(fx.ctx, "p:1", 1, 10, "kinodom")
	fx.s.Rate(fx.ctx, "p:2", 4, 10, "kinodom")
	rs, _ := fx.s.Recommend(fx.ctx, "p:1", Query{Moods: []string{"cozy"}, Limit: 3})
	if got := titles(rs); len(got) == 0 || got[0] != "Паддингтон" || slices.Contains(got, "Пила") {
		t.Fatalf("уютное для любителя «Матрицы»: %v", got)
	}
	rs, err := fx.s.Group(fx.ctx, []string{"p:1", "p:2"}, Query{Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	got := titles(rs)
	if len(got) == 0 || got[0] != "Паддингтон" || slices.Contains(got, "Начало") {
		t.Fatalf("вместе: общее первым, «Начало» (второму не зайдёт) — нет: %v", got)
	}
}

// rt — маршрутизатор для теста: все маршруты в один ServeMux.
type rt struct{ *http.ServeMux }

func (r rt) Handle(p, _ string, h http.Handler)      { r.ServeMux.Handle(p, h) }
func (r rt) HandleHome(p, _ string, h http.Handler)  { r.ServeMux.Handle(p, h) }
func (r rt) HandleLocal(p, _ string, h http.Handler) { r.ServeMux.Handle(p, h) }

func TestHTTP(t *testing.T) {
	pack := testPack(t, films, [][3]float64{{1, 2, 0.3}, {6, 7, 0.3}}, map[int]map[string]float64{6: {"cozy": 0.9}, 7: {"cozy": 0.8}})
	fx := newFixture(t, pack)
	mux := rt{http.NewServeMux()}
	fx.s.Register(mux, Env{
		Owner:  func(r *http.Request) string { return "p:1" },
		People: func(context.Context) []Person { return []Person{{"p:1", "Иван"}, {"p:2", "Маша"}} },
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	post := func(path, body string) int {
		resp, err := http.Post(srv.URL+path, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if c := post("/api/v1/recs/rate", `{"kpId":1,"score":10}`); c != 204 {
		t.Fatalf("оценка: %d", c)
	}
	if c := post("/api/v1/recs/mark", `{"kpId":3,"mark":"notmine"}`); c != 204 {
		t.Fatalf("отметка: %d", c)
	}
	var out struct {
		Items []struct {
			Film   FilmOut `json:"film"`
			Reason string  `json:"reason"`
		} `json:"items"`
		Query Query `json:"query"`
	}
	resp, err := http.Get(srv.URL + "/api/v1/recs?q=" + url.QueryEscape("что-нибудь уютное, без ужасов"))
	if err != nil {
		t.Fatal(err)
	}
	json.NewDecoder(resp.Body).Decode(&out)
	resp.Body.Close()
	if !slices.Contains(out.Query.Moods, "cozy") || !slices.Contains(out.Query.NoGenres, "ужасы") {
		t.Fatalf("разбор просьбы: %+v", out.Query)
	}
	if len(out.Items) == 0 || out.Items[0].Film.Title != "Паддингтон" || out.Items[0].Film.Poster != "/api/v1/recs/films/6/poster" {
		t.Fatalf("подборка: %+v", out.Items)
	}
	resp, _ = http.Get(srv.URL + "/api/v1/recs")
	json.NewDecoder(resp.Body).Decode(&out)
	resp.Body.Close()
	if len(out.Items) == 0 || out.Items[0].Film.Title != "Начало" || out.Items[0].Reason != "Похоже на «Матрица»" {
		t.Fatalf("без просьбы: %+v", out.Items)
	}
}
