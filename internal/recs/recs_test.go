package recs

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"encoding/json"
	"errors"

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
	fx.h.SetClock(func() time.Time { return *fx.now })
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

// Пакет установщика и ручной: берётся более свежий по дате сборки, отсутствующий файл не мешает.
func TestOpenNewest(t *testing.T) {
	older, newer := testPack(t, films, nil, nil), testPack(t, map[int]string{1: "Новый"}, nil, nil)
	for p, built := range map[string]string{older: "2026-10-01T00:00:00Z", newer: "2026-10-29T00:00:00Z"} {
		db, _ := sql.Open("sqlite", p)
		db.Exec(`INSERT INTO meta VALUES ('built', ?)`, built)
		db.Close()
	}
	m, err := OpenNewest(filepath.Join(t.TempDir(), "нет.db"), older, newer)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if f, ok := m.Film(context.Background(), 1); !ok || f.Title != "Новый" {
		t.Fatalf("открыт не самый свежий пакет: %+v", f)
	}
	if _, err := OpenNewest(filepath.Join(t.TempDir(), "нет.db")); !errors.Is(err, ErrNoModel) {
		t.Fatalf("без пакетов — ErrNoModel: %v", err)
	}
}

// Пакет несёт карточки только популярных фильмов: остальным Kinodom дозапрашивает карточку сам, а
// отсутствующие у Кинопоиска неделю не спрашивает.
func TestCardsFetchedOnDemand(t *testing.T) {
	pack := testPack(t, films, [][3]float64{{1, 2, 0.3}, {1, 3, 0.2}}, nil)
	db, _ := sql.Open("sqlite", pack)
	db.Exec(`UPDATE films SET poster = '', title_ru = '', title_orig = 'Inception' WHERE kp_id = 2`)
	db.Exec(`UPDATE films SET poster = '' WHERE kp_id = 3`)
	db.Close()
	fx := newFixture(t, pack)
	var mu sync.Mutex
	asked := map[int]int{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fx.s.StartCards(ctx, func(_ context.Context, kp int, _ bool) (Card, error) {
		mu.Lock()
		asked[kp]++
		mu.Unlock()
		if kp == 3 {
			return Card{}, ErrNoCard
		}
		return Card{Kind: "film", Title: "Начало", Poster: "https://x/inception.jpg", RatingKP: 8.7}, nil
	})
	fx.s.Rate(fx.ctx, "d:tv", 1, 10, "kinodom")
	rs, _ := fx.s.Recommend(fx.ctx, "d:tv", Query{Limit: 2})
	var show []Film
	for _, r := range rs {
		show = append(show, r.Film)
	}
	fx.s.needCards(show)
	deadline := time.Now().Add(10 * time.Second)
	for {
		mu.Lock()
		n := asked[2] + asked[3]
		mu.Unlock()
		if n >= 2 || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	f, ok := fx.s.film(fx.ctx, fx.s.Model(), 2)
	if !ok || f.Title != "Начало" || f.Poster == "" || f.RatingKP != 8.7 {
		t.Fatalf("дозапрошенная карточка не легла поверх пакета: %+v", f)
	}
	// второй показ: у «Начала» карточка есть, «Довода» нет у Кинопоиска — никого не спрашиваем снова
	fs, _ := fx.s.films(fx.ctx, fx.s.Model(), []int{2, 3})
	fx.s.needCards([]Film{fs[2], fs[3]})
	time.Sleep(200 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if asked[2] != 1 || asked[3] != 1 {
		t.Fatalf("повторные запросы: %v", asked)
	}
}

// Сериалы в общей подборке: у сериала связей меньше (Кинопоиск, а не MovieLens), и сумма вкладов меньше.
// Поклоннику сериалов они должны попадать в «Всё» наравне с фильмами, а тому, кто их не смотрит, —
// не пролезать шумом.
func TestSeriesMixFairly(t *testing.T) {
	fs := map[int]string{}
	var links [][3]float64
	for i := 1; i <= 30; i++ { // фильмы 1..30: у любимых — много сильных связей
		fs[i] = "Фильм"
		for j := 1; j <= 30; j++ {
			if i != j {
				links = append(links, [3]float64{float64(i), float64(j), 0.02 / (1 + 0.1*float64(j))}) // убывающие, как в графе
			}
		}
	}
	for i := 101; i <= 110; i++ { // сериалы: связи слабее и короче
		fs[i] = "Сериал"
		for j := 101; j <= 110; j++ {
			if i != j {
				links = append(links, [3]float64{float64(i), float64(j), 0.005 / (1 + 0.1*float64(j-100))})
			}
		}
	}
	links = append(links, [3]float64{1, 105, 0.001}) // фильм тянет за собой один сериал — это шум
	fx := newFixture(t, testPack(t, fs, links, nil))
	for _, kp := range []int{1, 2, 3, 4} {
		fx.s.Rate(fx.ctx, "p:fan", kp, 10, "kinodom")
		fx.s.Rate(fx.ctx, "p:films", kp, 10, "kinodom")
	}
	for _, kp := range []int{101, 102} {
		fx.s.Rate(fx.ctx, "p:fan", kp, 10, "kinodom")
	}
	count := func(owner string) int {
		rs, err := fx.s.Recommend(fx.ctx, owner, Query{Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, r := range rs {
			if r.Film.Kind == "series" {
				n++
			}
		}
		return n
	}
	if n := count("p:fan"); n < 2 {
		t.Fatalf("поклоннику сериалов — сериалы в общей подборке: %d из 10", n)
	}
	if n := count("p:films"); n != 0 {
		t.Fatalf("кто сериалы не смотрит — без шумных сериалов: %d из 10", n)
	}
}

// Сериал из оценок, которого нет в пакете: Kinodom дозапрашивает его «Похожие», и похожий сериал вне
// пакета попадает в подборку с причиной — «Похоже на „Папины дочки“».
func TestSimilarsForUnlinked(t *testing.T) {
	fx := newFixture(t, testPack(t, films, [][3]float64{{1, 2, 0.016}}, nil)) // вес — как у EASE в пакете
	const daughters, kitchen = 900001, 900002                                 // «Папины дочки» и «Кухня»: ни того, ни другого в пакете нет
	db := fx.s.DB
	db.W.Exec(`INSERT INTO rec_cards (kp_id, kind, title, poster, full, fetched_at) VALUES (?, 'series', 'Папины дочки', 'https://x/d.jpg', 1, 1)`, daughters)
	var mu sync.Mutex
	asked := 0
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fx.s.StartSimilars(ctx, func(_ context.Context, kp int) ([]SimilarItem, error) {
		mu.Lock()
		asked++
		mu.Unlock()
		if kp != daughters {
			return nil, ErrNoCard
		}
		return []SimilarItem{{KP: kitchen, Title: "Кухня", Poster: "https://x/k.jpg"}}, nil
	})
	fx.s.Rate(fx.ctx, "d:tv", daughters, 10, "kp")
	fx.s.Rate(fx.ctx, "d:tv", 1, 9, "kp")
	fx.s.Recommend(fx.ctx, "d:tv", Query{Limit: 5}) // первая подборка ставит «Похожие» в очередь
	deadline := time.Now().Add(10 * time.Second)
	for {
		var n int
		db.R.QueryRow(`SELECT COUNT(*) FROM rec_similars WHERE kp_id = ?`, daughters).Scan(&n)
		if n > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	rs, err := fx.s.Recommend(fx.ctx, "d:tv", Query{Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	var found *Rec
	for i := range rs {
		if rs[i].Film.KP == kitchen {
			found = &rs[i]
		}
	}
	if found == nil || found.Film.Title != "Кухня" || !slices.Contains(found.Because, daughters) {
		t.Fatalf("сериал вне пакета из «Похожих»: %+v", rs)
	}
	// «Матрица» связана в пакете — её «Похожие» не спрашиваем: ключ общий с рейтингами каталога
	mu.Lock()
	defer mu.Unlock()
	if asked != 1 {
		t.Fatalf("запросов «Похожих»: %d, ждали 1", asked)
	}
}

// Любимый жанр — не самый частый (драма есть почти у всего), а тот, что чаще, чем в кино вообще.
func TestGenreTasteLift(t *testing.T) {
	fs := map[int]string{}
	var links [][3]float64
	for i := 1; i <= 60; i++ {
		fs[i] = "Ф"
	}
	pack := testPack(t, fs, links, nil)
	db, _ := sql.Open("sqlite", pack)
	// каталог: драм много (40 из 60), фантастики мало (8), остальное — комедии
	for i := 1; i <= 60; i++ {
		g := "комедия"
		switch {
		case i <= 8:
			g = "фантастика, драма"
		case i <= 40:
			g = "драма"
		}
		db.Exec(`UPDATE films SET genres = ? WHERE kp_id = ?`, g, i)
	}
	db.Close()
	fx := newFixture(t, pack)
	// любит 5 фантастических (они же драмы) и 5 обычных драм
	for _, kp := range []int{1, 2, 3, 4, 5, 20, 21, 22, 23, 24} {
		fx.s.Rate(fx.ctx, "p:1", kp, 10, "kinodom")
	}
	gs, err := fx.s.GenreTaste(fx.ctx, "p:1")
	if err != nil {
		t.Fatal(err)
	}
	if len(gs) < 2 || gs[0].Genre != "фантастика" || gs[0].Title != "Фантастика" {
		t.Fatalf("любимый жанр — фантастика, а не драма: %+v", gs)
	}
}

// Выбран жанр, а соседей любимого в нём нет: подборка — из лучшего в жанре, а не пусто или три фильма.
func TestGenreFillsBeyondNeighbors(t *testing.T) {
	fs := map[int]string{1: "Любимый"}
	var links [][3]float64
	for id := 2; id < 70; id++ { // 68 соседей-драм: кандидатов хватает, популярное не добирается
		fs[id] = fmt.Sprintf("Драма %d", id)
		links = append(links, [3]float64{1, float64(id), 0.1})
	}
	for id := 100; id < 130; id++ {
		fs[id] = fmt.Sprintf("Аниме %d", id)
	}
	pack := testPack(t, fs, links, nil)
	db, err := sql.Open("sqlite", pack)
	if err != nil {
		t.Fatal(err)
	}
	db.Exec(`UPDATE films SET genres = 'аниме, мультфильм' WHERE kp_id >= 100`)
	db.Exec(`UPDATE films SET genres = 'аниме, драма' WHERE kp_id = 69`) // и одно аниме среди соседей
	db.Close()
	fx := newFixture(t, pack)
	fx.s.Rate(fx.ctx, "p:1", 1, 10, "kinodom")
	fx.s.Rate(fx.ctx, "p:1", 2, 8, "kinodom")
	fx.s.Rate(fx.ctx, "p:1", 3, 8, "kinodom")
	rs, err := fx.s.Recommend(fx.ctx, "p:1", Query{Genres: []string{"аниме"}, Limit: 18})
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 18 || rs[0].Film.KP != 69 || rs[0].Fill {
		t.Fatalf("сосед-аниме первым, дальше лучшее в жанре: %v", titles(rs))
	}
	if !rs[1].Fill || rs[1].Film.KP != 100 { // лучшее по качеству и популярности
		t.Fatalf("добор жанра: %+v", rs[1])
	}
}
