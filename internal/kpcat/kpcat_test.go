package kpcat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"kinodom/internal/meta"
	"kinodom/internal/store"
)

var ctx = context.Background()

// fakeKP — списки Кинопоиска в памяти: ключ списка (slug|фильтры|жанр|порядок) → фильмы; IMDb по номеру.
type fakeKP struct {
	mu      sync.Mutex
	lists   map[string][]meta.ListFilm
	failAt  map[string]int // ключ списка → смещение, на котором ответ — ошибка
	failErr error          // какая ошибка на failAt; nil — «Кинопоиск не отвечает» без паузы ворот
	imdb    map[int]float64
	imdbErr map[int]error // номер → ошибка оценки (сбой одного фильма)
	classes map[meta.KPClass]int
	calls   []string
	asked   []int // номера, у которых спросили IMDb
}

func listKey(q meta.ListQuery) string {
	return fmt.Sprintf("%s|%s|%s|%s", q.Slug, strings.Join(q.Bool, ","), q.Genre, q.Order)
}

func (f *fakeKP) List(_ context.Context, class meta.KPClass, q meta.ListQuery) ([]meta.ListFilm, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := listKey(q)
	f.calls = append(f.calls, fmt.Sprintf("%s@%d", k, q.Offset))
	if f.classes == nil {
		f.classes = map[meta.KPClass]int{}
	}
	f.classes[class]++
	if at, ok := f.failAt[k]; ok && q.Offset >= at {
		if f.failErr != nil {
			return nil, 0, f.failErr
		}
		return nil, 0, errors.New("Кинопоиск не отвечает")
	}
	all := f.lists[k]
	from := min(q.Offset, len(all))
	to := min(from+q.Limit, len(all))
	return slices.Clone(all[from:to]), len(all), nil
}

func (f *fakeKP) IMDb(_ context.Context, id int) (float64, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, id)
	if err := f.imdbErr[id]; err != nil {
		return 0, 0, err
	}
	return f.imdb[id], 100, nil
}

// films — n фильмов с номерами from…, оценка убывает с номером, год и премьера — по кругу.
func films(from, n int) []meta.ListFilm {
	var out []meta.ListFilm
	for i := range n {
		id := from + i
		out = append(out, meta.ListFilm{ID: id, Type: "FILM", NameRu: fmt.Sprintf("Фильм %d", id), Year: 2000 + i%20,
			Rating: 9 - float64(i)/100, Votes: 1000 - i, Poster: fmt.Sprintf("https://avatars.example/%d/300x450", id)})
	}
	return out
}

func newModule(t *testing.T, kp *fakeKP) *Module {
	t.Helper()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "kinodom.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return New(Options{DB: db, KP: kp, Posters: func(_ context.Context, src string) (string, error) {
		return "key-" + filepath.Base(filepath.Dir(src)), nil
	}})
}

func fullKP() *fakeKP {
	return &fakeKP{failAt: map[string]int{}, imdb: map[int]float64{}, lists: map[string][]meta.ListFilm{
		"popular-films|russian||POSITION_ASC":    films(1000, 60),
		"popular-films|foreign||POSITION_ASC":    films(2000, 120),
		"popular-series|russian||POSITION_ASC":   films(3000, 10),
		"popular-series|foreign||POSITION_ASC":   films(4000, 10),
		"|released|documentary|VOTES_COUNT_DESC": films(5000, 600),
	}}
}

func ids(vs []FilmView) []int {
	var out []int
	for _, v := range vs {
		out = append(out, v.ID)
	}
	return out
}

// План 14Г: обновление — пять разделов по своим спискам Кинопоиска, страницами по 50 до конца списка;
// документальные — не больше 500.
func TestRefreshSections(t *testing.T) {
	kp := fullKP()
	m := newModule(t, kp)
	if err := m.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	for sec, want := range map[string]int{"films-ru": 60, "films-foreign": 120, "series-ru": 10, "series-foreign": 10, "docs": 500} {
		vs, total, err := m.list(ctx, sec, "popular", 0, 1000)
		if err != nil || total != want || len(vs) != want {
			t.Errorf("%s: %d из %d, %v", sec, len(vs), total, err)
		}
	}
	vs, _, _ := m.list(ctx, "films-foreign", "popular", 0, 3)
	if !slices.Equal(ids(vs), []int{2000, 2001, 2002}) {
		t.Fatalf("места: %v", ids(vs))
	}
	n := 0
	for _, c := range kp.calls {
		if strings.HasPrefix(c, "|released|documentary|") {
			n++
		}
	}
	if n != 10 {
		t.Fatalf("документальные — 10 страниц по 50, а не %d", n)
	}
}

// Сайт отказал посреди раздела — прежние места раздела целиком (Review Focus 1).
func TestRefreshKeepsOldOnFailure(t *testing.T) {
	kp := fullKP()
	m := newModule(t, kp)
	if err := m.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	kp.mu.Lock()
	kp.lists["popular-films|foreign||POSITION_ASC"] = films(7000, 120)
	kp.failAt["popular-films|foreign||POSITION_ASC"] = 50
	kp.mu.Unlock()
	if err := m.Refresh(ctx); err == nil {
		t.Fatal("отказ сайта — нужна ошибка")
	}
	vs, total, _ := m.list(ctx, "films-foreign", "popular", 0, 1000)
	if total != 120 || vs[0].ID != 2000 {
		t.Fatalf("раздел после отказа: %d, первый %d", total, vs[0].ID)
	}
	if _, total, _ := m.list(ctx, "series-ru", "popular", 0, 1000); total != 10 {
		t.Fatalf("другой раздел: %d", total)
	}
}

// Порядки: популярность — место; КП — оценка; IMDb — без оценки в конце (Review Focus 2); новизна — дата
// выхода, без даты в конце (Review Focus 3).
func TestOrders(t *testing.T) {
	kp := fullKP()
	fs := films(100, 4)
	fs[0].Rating, fs[1].Rating, fs[2].Rating, fs[3].Rating = 6, 8, 7, 9
	fs[1].Premiere = time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	fs[2].Premiere = time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	fs[3].Premiere = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	kp.lists["popular-series|russian||POSITION_ASC"] = fs
	kp.imdb = map[int]float64{100: 7.5, 102: 8.2} // 101 и 103 — без IMDb
	m := newModule(t, kp)
	if err := m.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	for {
		did, err := m.fillIMDb(ctx, 50)
		if err != nil {
			t.Fatal(err)
		}
		if did == 0 {
			break
		}
	}
	for order, want := range map[string][]int{
		"popular": {100, 101, 102, 103}, "kp": {103, 101, 102, 100}, "imdb": {102, 100, 101, 103}, "new": {102, 101, 103, 100},
	} {
		vs, _, err := m.list(ctx, "series-ru", order, 0, 10)
		if err != nil || !slices.Equal(ids(vs), want) {
			t.Errorf("%s: %v, нужно %v (%v)", order, ids(vs), want, err)
		}
	}
	vs, _, _ := m.list(ctx, "series-ru", "imdb", 0, 10)
	if vs[0].IMDb != 8.2 || vs[2].IMDb != 0 {
		t.Fatalf("оценки IMDb на карточках: %+v", vs)
	}
}

// IMDb — сначала фильмы ближе к началу разделов; спрошенный без оценки повторно не спрашивается.
func TestIMDbFill(t *testing.T) {
	kp := fullKP()
	m := newModule(t, kp)
	if err := m.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if did, err := m.fillIMDb(ctx, 5); err != nil || did != 5 {
		t.Fatalf("порция: %d, %v", did, err)
	}
	kp.mu.Lock()
	first := slices.Clone(kp.asked)
	kp.mu.Unlock()
	for _, id := range first {
		if id%1000 > 1 { // места 0–1 каждого раздела идут первыми
			t.Fatalf("первыми спрошены %v", first)
		}
	}
	for {
		did, err := m.fillIMDb(ctx, 200)
		if err != nil {
			t.Fatal(err)
		}
		if did == 0 {
			break
		}
	}
	kp.mu.Lock()
	n := len(kp.asked)
	kp.mu.Unlock()
	if n != 700 {
		t.Fatalf("спрошено %d, фильмов 700 — каждый один раз", n)
	}
}

func getJSON(t *testing.T, h http.Handler, url string, v any) int {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", url, nil))
	if v != nil && rec.Code == 200 {
		if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
			t.Fatal(err)
		}
	}
	return rec.Code
}

type mux struct{ *http.ServeMux }

func (m mux) Handle(p, _ string, h http.Handler) { m.ServeMux.Handle(p, h) }

// Маршруты: порция по смещению, всего и «есть ещё»; неизвестный раздел — 400; пустая база — пустой список
// (Review Focus 5).
func TestListRoute(t *testing.T) {
	m := newModule(t, fullKP())
	h := mux{http.NewServeMux()}
	m.Register(h)
	var lv ListView
	if code := getJSON(t, h, "/api/v1/kpcat/list?section=films-ru&order=kp", &lv); code != 200 || len(lv.Entries) != 0 || lv.More {
		t.Fatalf("пустая база: %d %+v", code, lv)
	}
	if err := m.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if code := getJSON(t, h, "/api/v1/kpcat/list?section=films-ru&order=popular&offset=48", &lv); code != 200 || lv.Total != 60 || len(lv.Entries) != 12 || lv.More {
		t.Fatalf("хвост: %d, %d из %d, ещё %v", code, len(lv.Entries), lv.Total, lv.More)
	}
	if lv.Entries[0].Poster != "/api/v1/kpcat/films/1048/poster" {
		t.Fatalf("постер: %q", lv.Entries[0].Poster)
	}
	if code := getJSON(t, h, "/api/v1/kpcat/list?section=films-ru&offset=0", &lv); code != 200 || len(lv.Entries) != PageSize || !lv.More {
		t.Fatalf("первая порция: %d, %d, ещё %v", code, len(lv.Entries), lv.More)
	}
	if code := getJSON(t, h, "/api/v1/kpcat/list?section=cartoons", nil); code != 400 {
		t.Fatalf("неизвестный раздел: %d", code)
	}
	var cv CatalogView
	if code := getJSON(t, h, "/api/v1/kpcat", &cv); code != 200 || len(cv.Sections) != 5 || len(cv.Orders) != 4 || cv.Sections[0].Count != 60 {
		t.Fatalf("каталог: %d %+v", code, cv)
	}
	var f FilmView
	if code := getJSON(t, h, "/api/v1/kpcat/films/1001", &f); code != 200 || f.Title != "Фильм 1001" {
		t.Fatalf("фильм: %d %+v", code, f)
	}
	if code := getJSON(t, h, "/api/v1/kpcat/films/9", nil); code != 404 {
		t.Fatalf("нет фильма: %d", code)
	}
}

// Постер — в кэш картинок по адресу аватара, ответ — переход на /img/{key}.
func TestPosterRoute(t *testing.T) {
	m := newModule(t, fullKP())
	if err := m.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	h := mux{http.NewServeMux()}
	m.Register(h)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/kpcat/films/1001/poster", nil))
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/img/key-1001" {
		t.Fatalf("постер: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/kpcat/films/9/poster", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("нет фильма: %d", rec.Code)
	}
}

// Ревью 14Г, Important 1: раздел не обновился — цикл не повторяет все разделы раз за разом (суточный предел
// общий с медиатекой и правкой); каталог ходит очередью KPList (без резерва).
func TestRunLoopDoesNotHammer(t *testing.T) {
	kp := fullKP()
	kp.failAt = map[string]int{"|released|documentary|VOTES_COUNT_DESC": 0} // документальные всегда сбоят
	m := newModule(t, kp)
	m.o.FirstDelay, m.o.Every = 10*time.Millisecond, time.Hour
	runCtx, cancel := context.WithTimeout(ctx, 2500*time.Millisecond)
	defer cancel()
	m.Run(runCtx)
	kp.mu.Lock()
	n, classes := len(kp.calls), kp.classes
	kp.mu.Unlock()
	if n > 8 { // один проход: 2+3+1+1 страницы и одна неудачная
		t.Fatalf("запросов списков за 2,5 с: %d", n)
	}
	if classes[meta.KPList] != n {
		t.Fatalf("очереди: %v", classes)
	}
}

// Обновляются только просроченные разделы; раздел, где пришло заметно меньше, чем сайт обещал, — не заменяется.
func TestRefreshDueAndTruncated(t *testing.T) {
	kp := fullKP()
	m := newModule(t, kp)
	if err := m.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := m.st.W.Exec(`UPDATE kpcat_state SET refreshed_at = 1 WHERE section = 'films-foreign'`); err != nil {
		t.Fatal(err)
	}
	kp.mu.Lock()
	kp.calls = nil
	kp.mu.Unlock()
	if err := m.refreshDue(ctx); err != nil {
		t.Fatal(err)
	}
	kp.mu.Lock()
	calls := slices.Clone(kp.calls)
	kp.mu.Unlock()
	for _, c := range calls {
		if !strings.HasPrefix(c, "popular-films|foreign|") {
			t.Fatalf("обновлён не просроченный раздел: %v", calls)
		}
	}
	// Сайт обещает 120, а отдаёт 40 и пустую страницу — раздел остаётся прежним.
	kp.mu.Lock()
	kp.lists["popular-films|foreign||POSITION_ASC"] = films(7000, 40)
	kp.mu.Unlock()
	m.o.KP = truncKP{kp, 120}
	if err := m.Refresh(ctx); err == nil {
		t.Fatal("обрезанный раздел — нужна ошибка")
	}
	if vs, total, _ := m.list(ctx, "films-foreign", "popular", 0, 1); total != 120 || vs[0].ID != 2000 {
		t.Fatalf("раздел: %d, первый %d", total, vs[0].ID)
	}
}

// truncKP — сайт обещает total, а отдаёт меньше.
type truncKP struct {
	*fakeKP
	total int
}

func (f truncKP) List(c context.Context, class meta.KPClass, q meta.ListQuery) ([]meta.ListFilm, int, error) {
	rs, _, err := f.fakeKP.List(c, class, q)
	return rs, f.total, err
}

// Ревью 14Г, Important 3: сбой оценки одного фильма не останавливает очередь — повтор через сутки.
func TestIMDbFillSkipsBrokenFilm(t *testing.T) {
	kp := fullKP()
	kp.imdbErr = map[int]error{1000: errors.New("оценки IMDb: ответ 404")}
	m := newModule(t, kp)
	if err := m.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err := m.fillIMDb(ctx, 5); err != nil {
			t.Fatal(err)
		}
	}
	kp.mu.Lock()
	asked := slices.Clone(kp.asked)
	kp.mu.Unlock()
	n := 0
	for _, id := range asked {
		if id == 1000 {
			n++
		}
	}
	if n != 1 || len(asked) != 15 {
		t.Fatalf("сбойный спрошен %d раз, всего %d: %v", n, len(asked), asked)
	}
}

// Ревью 14Г, Important 4: в «Новых» ещё не вышедшие (премьера впереди) — в конце, как без даты.
func TestNewSkipsFuturePremieres(t *testing.T) {
	kp := fullKP()
	now := time.Now()
	fs := films(100, 3)
	fs[0].Premiere = now.AddDate(0, 3, 0) // анонс
	fs[1].Premiere = now.AddDate(0, 0, -10)
	fs[2].Premiere = now.AddDate(-1, 0, 0)
	kp.lists["popular-series|russian||POSITION_ASC"] = fs
	m := newModule(t, kp)
	if err := m.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	vs, _, _ := m.list(ctx, "series-ru", "new", 0, 10)
	if !slices.Equal(ids(vs), []int{101, 102, 100}) {
		t.Fatalf("новые: %v", ids(vs))
	}
}

// Ревью 14Г, Important 5: ключи постеров каталога — чистка кэша картинок их не удаляет.
func TestImageKeys(t *testing.T) {
	m := newModule(t, fullKP())
	if err := m.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	keys, err := m.ImageKeys(ctx)
	if err != nil || len(keys) != 700 || !keys[meta.ImageKey("https://avatars.example/1001/300x450")] {
		t.Fatalf("ключи: %d, %v", len(keys), err)
	}
}

// Ревью 14Г: «Каталог ещё пуст — идёт первое обновление» висел и при неудачном обновлении, а «обновлён» был
// максимумом по разделам. У раздела — своё время обновления и ошибка последней попытки; удачная её снимает.
func TestCatalogSectionState(t *testing.T) {
	kp := fullKP()
	kp.failAt = map[string]int{"|released|documentary|VOTES_COUNT_DESC": 0}
	m := newModule(t, kp)
	h := mux{http.NewServeMux()}
	m.Register(h)
	m.Refresh(ctx)
	by := sectionsOf(t, h)
	if d := by["docs"]; d.UpdatedAt != nil || d.Error == "" || d.Count != 0 {
		t.Fatalf("документальные: %+v", d)
	}
	if f := by["films-ru"]; f.UpdatedAt == nil || f.Error != "" {
		t.Fatalf("русские фильмы: %+v", f)
	}
	kp.mu.Lock()
	kp.failAt = map[string]int{}
	kp.mu.Unlock()
	if err := m.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	for id, s := range sectionsOf(t, h) {
		if s.Error != "" || s.UpdatedAt == nil {
			t.Fatalf("после удачного %s: %+v", id, s)
		}
	}
}

// Review Focus 2 (15В): ворота на паузе — у всех несделанных разделов её текст, а не «идёт первое обновление» на 6 ч.
func TestCatalogPausedSections(t *testing.T) {
	kp := fullKP()
	kp.failAt, kp.failErr = map[string]int{"popular-films|russian||POSITION_ASC": 0}, meta.ErrKPBlocked
	m := newModule(t, kp)
	h := mux{http.NewServeMux()}
	m.Register(h)
	if err := m.Refresh(ctx); !errors.Is(err, meta.ErrKPBlocked) {
		t.Fatalf("обновление: %v", err)
	}
	for id, s := range sectionsOf(t, h) {
		if s.Error != meta.ErrKPBlocked.Error() {
			t.Fatalf("%s: %+v", id, s)
		}
	}
}

// Ревью 14Г: пауза ворот — уже строка в журнале у ворот; каталог не пишет «не удалось» на каждую попытку.
func TestRunQuietWhilePaused(t *testing.T) {
	kp := fullKP()
	kp.failAt, kp.failErr = map[string]int{"popular-films|russian||POSITION_ASC": 0}, meta.ErrKPBlocked
	m := newModule(t, kp)
	var buf bytes.Buffer
	m.log = slog.New(slog.NewTextHandler(&buf, nil))
	m.o.FirstDelay = 10 * time.Millisecond
	runCtx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	m.Run(runCtx)
	kp.mu.Lock()
	n := len(kp.calls)
	kp.mu.Unlock()
	if n == 0 {
		t.Fatal("обновления не было")
	}
	if buf.Len() > 0 {
		t.Fatalf("журнал: %s", buf.String())
	}
}

// sectionsOf — разделы /api/v1/kpcat по номеру.
func sectionsOf(t *testing.T, h http.Handler) map[string]SectionView {
	t.Helper()
	var cv CatalogView
	if code := getJSON(t, h, "/api/v1/kpcat", &cv); code != 200 {
		t.Fatalf("каталог: %d", code)
	}
	out := map[string]SectionView{}
	for _, s := range cv.Sections {
		out[s.ID] = s
	}
	return out
}

// Ревью 14Г: без даты выхода, но с годом — в «Новых» по году (1 января), а не в самом хвосте; год впереди — к
// анонсам в конец.
func TestNewByYearWithoutDate(t *testing.T) {
	kp := fullKP()
	now := time.Now()
	y := now.Year()
	fs := films(100, 4)
	fs[0].Year = y - 1 // без даты, прошлый год
	fs[1].Premiere, fs[1].Year = now.AddDate(0, 0, -10), y
	fs[2].Year = y + 1 // без даты, следующий год — анонс
	fs[3].Premiere, fs[3].Year = time.Date(y-3, 6, 1, 0, 0, 0, 0, time.UTC), y-3
	kp.lists["popular-series|russian||POSITION_ASC"] = fs
	m := newModule(t, kp)
	if err := m.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	vs, _, _ := m.list(ctx, "series-ru", "new", 0, 10)
	if !slices.Equal(ids(vs), []int{101, 100, 103, 102}) {
		t.Fatalf("новые: %v", ids(vs))
	}
}

// Ревью 14Г: подгрузка по смещению повторяла и пропускала фильмы, когда порядок менялся на ходу (пришли
// оценки IMDb, суточное обновление). Порции листают порядок, снятый первой порцией (snap); неизвестный snap
// (сервер перезапущен) — живой порядок под новым снимком, без ошибки (Review Focus 1, 15В).
func TestListSnapshotKeepsOrder(t *testing.T) {
	m := newModule(t, fullKP())
	if err := m.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	h := mux{http.NewServeMux()}
	m.Register(h)
	var first ListView
	if code := getJSON(t, h, "/api/v1/kpcat/list?section=films-ru&order=imdb&limit=24", &first); code != 200 || first.Snap == "" {
		t.Fatalf("первая порция: %d, snap %q", code, first.Snap)
	}
	// Пока человек смотрит первую порцию, у фильмов из хвоста пришли оценки IMDb — в живом порядке они первые.
	for id := 1040; id < 1060; id++ {
		if err := m.st.saveIMDb(ctx, id, 9.9, 100, m.now()); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[int]int{}
	for _, f := range first.Entries {
		seen[f.ID]++
	}
	offset := len(first.Entries)
	for more := first.More; more; {
		var v ListView
		getJSON(t, h, fmt.Sprintf("/api/v1/kpcat/list?section=films-ru&order=imdb&limit=24&offset=%d&snap=%s", offset, first.Snap), &v)
		for _, f := range v.Entries {
			seen[f.ID]++
		}
		offset += len(v.Entries)
		more = v.More && len(v.Entries) > 0
	}
	if len(seen) != 60 {
		t.Fatalf("показано %d из 60", len(seen))
	}
	for id, n := range seen {
		if n > 1 {
			t.Fatalf("фильм %d показан %d раза", id, n)
		}
	}
	var lost ListView
	if code := getJSON(t, h, "/api/v1/kpcat/list?section=films-ru&order=imdb&limit=24&offset=24&snap=nope", &lost); code != 200 ||
		len(lost.Entries) != 24 || lost.Snap == "" || lost.Snap == "nope" {
		t.Fatalf("неизвестный снимок: %d, %d, %q", code, len(lost.Entries), lost.Snap)
	}
}

// Ревью 15В, Important 1: в пустом разделе — текст для человека: сетевая ошибка (по-английски, с адресом) —
// «Кинопоиск не отвечает», подробности — в журнале; суточный предел — как есть.
func TestCatalogSectionHumanError(t *testing.T) {
	kp := fullKP()
	kp.failAt = map[string]int{"popular-films|russian||POSITION_ASC": 0}
	kp.failErr = errors.New("Кинопоиск: dial tcp 127.0.0.1:1: connectex: No connection could be made because the target machine actively refused it.")
	m := newModule(t, kp)
	h := mux{http.NewServeMux()}
	m.Register(h)
	m.Refresh(ctx)
	if e := sectionsOf(t, h)["films-ru"].Error; e != meta.ErrKPBlocked.Error() {
		t.Fatalf("сетевая ошибка: %q", e)
	}
	kp.mu.Lock()
	kp.failErr = meta.ErrKPDailyLimit
	kp.mu.Unlock()
	m.Refresh(ctx)
	if e := sectionsOf(t, h)["films-ru"].Error; e != meta.ErrKPDailyLimit.Error() {
		t.Fatalf("суточный предел: %q", e)
	}
}

// Ревью 15В, Important 2: каталог молчит только о паузах ворот (их пишут сами ворота); проход, где кроме паузы
// есть другая ошибка, — в журнал.
func TestPausedOnlyWhenAllPauses(t *testing.T) {
	for _, c := range []struct {
		err  error
		want bool
	}{
		{meta.ErrKPBlocked, true},
		{meta.ErrKPDailyLimit, true},
		{errors.Join(meta.ErrKPBlocked, meta.ErrKPBlocked), true},
		{errors.Join(errors.New("Кинопоиск: в разделе «Х» пришло 400 из 500"), meta.ErrKPBlocked), false},
		{errors.New("Кинопоиск: dial tcp"), false},
	} {
		if got := paused(c.err); got != c.want {
			t.Errorf("%v: %v, нужно %v", c.err, got, c.want)
		}
	}
}

// План 16А: постер «Кинопоиска» в сетке — миниатюрой: ?w= уходит в переход на /img/{key}.
func TestPosterRouteThumb(t *testing.T) {
	m := newModule(t, fullKP())
	if err := m.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	h := mux{http.NewServeMux()}
	m.Register(h)
	for url, want := range map[string]string{
		"/api/v1/kpcat/films/1001/poster?w=400": "/img/key-1001?w=400",
		"/api/v1/kpcat/films/1001/poster?w=x":   "/img/key-1001",
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", url, nil))
		if rec.Code != http.StatusFound || rec.Header().Get("Location") != want {
			t.Errorf("%s: %d %q", url, rec.Code, rec.Header().Get("Location"))
		}
	}
}
