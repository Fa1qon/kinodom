package meta

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"kinodom/internal/store"
)

// clock — подменные часы очереди.
type clock struct{ t time.Time }

func (c *clock) now() time.Time      { return c.t }
func (c *clock) add(d time.Duration) { c.t = c.t.Add(d) }

func newRatings(t *testing.T, f *fakeKP, key string) (*Ratings, *clock, *store.DB) {
	t.Helper()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "kinodom.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	c := &clock{t: time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)}
	r := NewRatings(RatingsOptions{KP: newKP(f, key), DB: db})
	r.now = c.now
	return r, c, db
}

// drain делает задачи, пока они есть.
func drain(t *testing.T, r *Ratings) {
	t.Helper()
	for i := 0; ; i++ {
		did, err := r.Step(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !did {
			return
		}
		if i > 100 {
			t.Fatal("очередь не кончается")
		}
	}
}

func enqueue(t *testing.T, r *Ratings, prio int, it Item) {
	t.Helper()
	if err := r.Enqueue(ctx, prio, it); err != nil {
		t.Fatal(err)
	}
}

func ratingOf(t *testing.T, r *Ratings, release string) (Rating, bool) {
	t.Helper()
	m, err := r.For(ctx, []string{release})
	if err != nil {
		t.Fatal(err)
	}
	got, ok := m[release]
	return got, ok
}

func queueLen(t *testing.T, r *Ratings) int {
	t.Helper()
	st, err := r.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return st.Queue
}

// Номер Кинопоиска из описания раздачи — без поиска, один запрос (спека, раздел 8).
func TestKnownKinopoiskIDCostsOneRequest(t *testing.T) {
	f := newFakeKP(t)
	r, _, _ := newRatings(t, f, testKey)
	enqueue(t, r, 1, Item{Release: "rutor:1", KinopoiskID: 301, Title: "Матрица / The Matrix (1999) BDRip"})
	drain(t, r)
	got, ok := ratingOf(t, r, "rutor:1")
	want := Rating{KinopoiskID: 301, Kinopoisk: 8.5, IMDb: 8.7, NameRu: "Матрица", NameOrig: "The Matrix", Year: 1999}
	if !ok || got != want {
		t.Fatalf("рейтинг %+v, %v", got, ok)
	}
	if f.Hits("film") != 1 || f.Hits("search")+f.Hits("imdb") != 0 {
		t.Fatalf("запросы: фильм %d, поиск %d, IMDb %d", f.Hits("film"), f.Hits("search"), f.Hits("imdb"))
	}
}

// Ссылки на Кинопоиск нет, есть на IMDb — один запрос, рейтинг прямо из ответа (хвост этапа 3).
func TestIMDbFindsFilmInOneRequest(t *testing.T) {
	f := newFakeKP(t)
	r, _, _ := newRatings(t, f, testKey)
	enqueue(t, r, 1, Item{Release: "rutracker:2", IMDbID: "tt0133093", Title: "Что-то невнятное"})
	drain(t, r)
	if got, ok := ratingOf(t, r, "rutracker:2"); !ok || got.KinopoiskID != 301 || got.Kinopoisk != 8.5 {
		t.Fatalf("рейтинг %+v, %v", got, ok)
	}
	if f.Hits("imdb") != 1 || f.Hits("film")+f.Hits("search") != 0 {
		t.Fatalf("запросы: IMDb %d, фильм %d, поиск %d", f.Hits("imdb"), f.Hits("film"), f.Hits("search"))
	}
}

// Десятки рипов одного фильма — один поиск: кэш по (названию, году) (спека, раздел 8).
func TestSameFilmDifferentRipsCostsOneRequest(t *testing.T) {
	f := newFakeKP(t)
	r, _, _ := newRatings(t, f, testKey)
	enqueue(t, r, 1, Item{Release: "rutor:1", Title: "Матрица / The Matrix (1999) BDRip от HQCLUB | Лицензия"})
	enqueue(t, r, 2, Item{Release: "rutor:2", Title: "Матрица / The Matrix (1999) HDRip от Scarabey"})
	enqueue(t, r, 3, Item{Release: "rutracker:3", Title: "Матрица / The Matrix (Лана Вачовски) [1999, США, фантастика, BDRip 1080p]"})
	drain(t, r)
	for _, rel := range []string{"rutor:1", "rutor:2", "rutracker:3"} {
		if got, ok := ratingOf(t, r, rel); !ok || got.KinopoiskID != 301 {
			t.Fatalf("%s: %+v, %v", rel, got, ok)
		}
	}
	if f.Hits("search") != 1 || f.Hits("film") != 0 {
		t.Fatalf("поисков %d, фильмов %d", f.Hits("search"), f.Hits("film"))
	}
}

// Поиск по названию — только совпадение названия и года: чужой фильм хуже, чем никакого.
func TestSearchNeedsSameName(t *testing.T) {
	f := newFakeKP(t)
	f.search["The Matrix Revisited"] = itemsJSON(filmJSON(999, "Совсем другое", "Something Else", 1999, 7.0))
	r, _, _ := newRatings(t, f, testKey)
	enqueue(t, r, 1, Item{Release: "rutor:9", Title: "Матрица: Возвращение / The Matrix Revisited (1999) DVDRip"})
	drain(t, r)
	if got, ok := ratingOf(t, r, "rutor:9"); ok {
		t.Fatalf("найден чужой фильм %+v", got)
	}
}

// Только русское название: поиск кириллицей отвечает 500 и тратит квоту — одна попытка, потом
// неделя тишины, даже если каталог ставит раздачу снова (исследование, раздел 12).
func TestCyrillicSearchFailureBacksOff(t *testing.T) {
	f := newFakeKP(t)
	r, c, _ := newRatings(t, f, testKey)
	it := Item{Release: "rutor:3", Title: "Холоп 3 (2026) WEBRip 1080p"}
	enqueue(t, r, 1, it)
	drain(t, r)
	if f.Hits("search") != 1 || queueLen(t, r) != 0 {
		t.Fatalf("поисков %d, в очереди %d", f.Hits("search"), queueLen(t, r))
	}
	c.add(24 * time.Hour)
	enqueue(t, r, 1, it) // каталог обновился
	drain(t, r)
	if f.Hits("search") != 1 {
		t.Fatalf("через сутки снова искали: поисков %d", f.Hits("search"))
	}
	c.add(7 * 24 * time.Hour)
	enqueue(t, r, 1, it)
	drain(t, r)
	if f.Hits("search") != 2 {
		t.Fatalf("через неделю: поисков %d", f.Hits("search"))
	}
}

// Не найдено — повтор через 30 дней, и для других раздач с тем же названием тоже.
func TestNotFoundIsRememberedForAMonth(t *testing.T) {
	f := newFakeKP(t)
	r, c, _ := newRatings(t, f, testKey)
	enqueue(t, r, 1, Item{Release: "rutor:4", Title: "Неизвестно / Unknown Thing (2020) WEB-DL"})
	enqueue(t, r, 2, Item{Release: "rutor:5", Title: "Неизвестно / Unknown Thing (2020) BDRip"})
	drain(t, r)
	if f.Hits("search") != 2 { // оригинальное и русское название, дальше — кэш
		t.Fatalf("поисков %d", f.Hits("search"))
	}
	c.add(31 * 24 * time.Hour)
	enqueue(t, r, 1, Item{Release: "rutor:4", Title: "Неизвестно / Unknown Thing (2020) WEB-DL"})
	drain(t, r)
	if f.Hits("search") != 4 {
		t.Fatalf("через месяц: поисков %d", f.Hits("search"))
	}
}

// Квота кончилась посреди очереди (402): раздачи с известным номером получают рейтинг без ключа
// (rating.kinopoisk.ru), остальные ждут; через час, когда квота вернулась, — дальше.
func TestQuotaPauseFallsBackToKeyless(t *testing.T) {
	f := newFakeKP(t)
	r, c, _ := newRatings(t, f, testKey)
	enqueue(t, r, 1, Item{Release: "rutor:1", KinopoiskID: 301})
	enqueue(t, r, 2, Item{Release: "rutor:2", Title: "Матрица / The Matrix (1999) BDRip"})
	f.SetStatus(http.StatusPaymentRequired)
	drain(t, r)
	if got, ok := ratingOf(t, r, "rutor:1"); !ok || got.Kinopoisk != 8.501 {
		t.Fatalf("рейтинг без ключа: %+v, %v", got, ok)
	}
	if queueLen(t, r) != 1 || f.Hits("search") != 0 {
		t.Fatalf("в очереди %d, поисков %d — без квоты искать нечем", queueLen(t, r), f.Hits("search"))
	}
	f.SetStatus(0)
	c.add(61 * time.Minute)
	drain(t, r)
	if got, ok := ratingOf(t, r, "rutor:2"); !ok || got.KinopoiskID != 301 {
		t.Fatalf("после паузы: %+v, %v", got, ok)
	}
}

// 429 — короткая пауза и повтор, задача не теряется.
func TestRateLimitedIsRetried(t *testing.T) {
	f := newFakeKP(t)
	r, c, _ := newRatings(t, f, testKey)
	enqueue(t, r, 1, Item{Release: "rutor:1", KinopoiskID: 301})
	f.SetStatus(http.StatusTooManyRequests)
	drain(t, r)
	if _, ok := ratingOf(t, r, "rutor:1"); ok || queueLen(t, r) != 1 {
		t.Fatal("после 429 задача должна ждать в очереди")
	}
	f.SetStatus(0)
	c.add(3 * time.Second)
	drain(t, r)
	if _, ok := ratingOf(t, r, "rutor:1"); !ok {
		t.Fatal("после паузы рейтинга нет")
	}
}

// Ключ не подходит — проблема в «Состоянии», рейтинги по номеру — без ключа.
func TestBadKeySetsProblemAndGoesKeyless(t *testing.T) {
	f := newFakeKP(t)
	r, _, db := newRatings(t, f, "wrong-key")
	r.checkQuota(ctx)
	ps, err := db.Problems(ctx)
	if err != nil || len(ps) != 1 || ps[0].ID != ProblemKinopoiskKey {
		t.Fatalf("проблемы %+v, %v", ps, err)
	}
	enqueue(t, r, 1, Item{Release: "rutor:1", KinopoiskID: 301})
	drain(t, r)
	if got, ok := ratingOf(t, r, "rutor:1"); !ok || got.Kinopoisk != 8.501 || f.Hits("film") != 0 {
		t.Fatalf("рейтинг %+v, %v, запросов к API %d", got, ok, f.Hits("film"))
	}
}

// Без ключа: рейтинги по номеру есть, раздачи без номера ждут ключа.
func TestWithoutKeyOnlyKnownIDs(t *testing.T) {
	f := newFakeKP(t)
	r, _, _ := newRatings(t, f, "")
	enqueue(t, r, 1, Item{Release: "rutor:1", KinopoiskID: 301})
	enqueue(t, r, 2, Item{Release: "rutor:2", Title: "Матрица / The Matrix (1999) BDRip"})
	drain(t, r)
	if _, ok := ratingOf(t, r, "rutor:1"); !ok {
		t.Fatal("рейтинга по номеру нет")
	}
	if queueLen(t, r) != 1 || f.Hits("search") != 0 {
		t.Fatalf("в очереди %d, поисков %d", queueLen(t, r), f.Hits("search"))
	}
}

// Квота тратится в порядке основного каталога: меньший prio — раньше.
func TestPriorityOrder(t *testing.T) {
	f := newFakeKP(t)
	r, _, _ := newRatings(t, f, testKey)
	enqueue(t, r, 5, Item{Release: "rutor:late", KinopoiskID: 301})
	enqueue(t, r, 1, Item{Release: "rutor:first", IMDbID: "tt0133093"})
	if did, err := r.Step(ctx); !did || err != nil {
		t.Fatal(did, err)
	}
	if f.Hits("imdb") != 1 || f.Hits("film") != 0 {
		t.Fatalf("первой сделана не та задача: IMDb %d, фильм %d", f.Hits("imdb"), f.Hits("film"))
	}
}

// Найденный фильм — навсегда, рейтинг — раз в 30 дней (спека, раздел 8).
func TestRatingIsRefreshedAfter30Days(t *testing.T) {
	f := newFakeKP(t)
	r, c, _ := newRatings(t, f, testKey)
	it := Item{Release: "rutor:1", KinopoiskID: 301}
	enqueue(t, r, 1, it)
	drain(t, r)
	enqueue(t, r, 1, it)
	drain(t, r)
	if f.Hits("film") != 1 {
		t.Fatalf("свежий рейтинг запрошен снова: %d", f.Hits("film"))
	}
	c.add(31 * 24 * time.Hour)
	enqueue(t, r, 1, it)
	drain(t, r)
	if f.Hits("film") != 2 {
		t.Fatalf("через 30 дней: %d", f.Hits("film"))
	}
}

// Фильм ещё не вышел — ratingKinopoisk = null: фильм найден, рейтинга нет, это не ошибка.
func TestNullRatingIsNoRating(t *testing.T) {
	f := newFakeKP(t)
	f.films[555] = filmJSON(555, "Новинка", "Novelty", 2026, nil)
	r, _, _ := newRatings(t, f, testKey)
	enqueue(t, r, 1, Item{Release: "rutor:7", KinopoiskID: 555})
	drain(t, r)
	if got, ok := ratingOf(t, r, "rutor:7"); !ok || got.KinopoiskID != 555 || got.Kinopoisk != 0 {
		t.Fatalf("рейтинг %+v, %v", got, ok)
	}
}

// Run: лимиты ключа — первым запросом, затем очередь; остановка — по отмене.
func TestRunChecksQuotaThenWorksQueue(t *testing.T) {
	f := newFakeKP(t)
	r, _, _ := newRatings(t, f, testKey)
	r.now = time.Now
	cctx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- r.Run(cctx) }()
	enqueue(t, r, 1, Item{Release: "rutor:1", KinopoiskID: 301})
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, ok := ratingOf(t, r, "rutor:1"); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("очередь не сработала за 5 с")
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	st, err := r.Status(ctx)
	if err != nil || st.Quota.DailyLimit != 500 || f.Hits("key") != 1 {
		t.Fatalf("состояние %+v, %v, запросов лимитов %d", st, err, f.Hits("key"))
	}
}

// У раздачи есть год — у фильма тоже должен быть и совпадать с точностью до года. Записи
// Кинопоиска без года (заглушки) иначе совпадают с любым годом: вживую «Бегущая / The Runner
// (2026)» получила 589920 «The Runner» без года вместо 6549627 (исследование, раздел 13).
func TestSearchNeedsYearWhenReleaseHasOne(t *testing.T) {
	f := newFakeKP(t)
	f.search["The Runner"] = itemsJSON(filmJSON(589920, "", "The Runner", 0, nil), filmJSON(6549627, "Бегущая", "The Runner", 2026, 6.1))
	r, _, _ := newRatings(t, f, testKey)
	enqueue(t, r, 1, Item{Release: "rutor:1104968", Title: "Бегущая / The Runner (2026) WEB-DL 1080p | P"})
	drain(t, r)
	if got, ok := ratingOf(t, r, "rutor:1104968"); !ok || got.KinopoiskID != 6549627 {
		t.Fatalf("фильм %+v, %v — нужен 6549627 (2026), а не запись без года", got, ok)
	}
}

// Поиск отстаёт от карточки фильма: у новинки в выдаче нет ни года, ни рейтинга (вживую —
// «Бегущая / The Runner» 6549627: в поиске год и рейтинг null, по номеру — 2026 и 6.1).
// Запись без года засчитывается, только если совпали оба названия раздачи, а рейтинг берётся
// по номеру — иначе 30 дней висел бы 0 (исследование, раздел 13).
func TestYearlessSearchResultNeedsBothNamesAndFullFilm(t *testing.T) {
	f := newFakeKP(t)
	f.search["The Runner"] = itemsJSON(filmJSON(589920, "", "The Runner", 0, nil), filmJSON(6549627, "Бегущая", "The Runner", 0, nil))
	f.films[6549627] = filmJSON(6549627, "Бегущая", "The Runner", 2026, 6.1)
	r, _, _ := newRatings(t, f, testKey)
	enqueue(t, r, 1, Item{Release: "rutor:1104968", Title: "Бегущая / The Runner (2026) WEB-DL 1080p | P"})
	drain(t, r)
	got, ok := ratingOf(t, r, "rutor:1104968")
	if !ok || got.KinopoiskID != 6549627 || got.Kinopoisk != 6.1 || got.Year != 2026 {
		t.Fatalf("фильм %+v, %v", got, ok)
	}
	if f.Hits("search") != 1 || f.Hits("film") != 1 {
		t.Fatalf("поисков %d, фильмов %d — нужен один поиск и один запрос фильма", f.Hits("search"), f.Hits("film"))
	}
}

// Сбой, который повторяется (битая запись у Кинопоиска отвечает 500 каждый раз), не тратит квоту
// каждые 5 минут: пауза растёт — 5 минут, час, сутки, неделя. Каждый ответ 5xx платный
// (исследование, раздел 12); без роста пауз вышло 288 запросов в сутки (ревью этапа 5b).
func TestRepeatingServerErrorBacksOff(t *testing.T) {
	f := newFakeKP(t)
	r, c, _ := newRatings(t, f, testKey)
	enqueue(t, r, 1, Item{Release: "rutor:1", IMDbID: "tt0133093"})
	f.SetStatus(http.StatusInternalServerError)
	for range 24 * 12 { // сутки по 5 минут
		drain(t, r)
		c.add(5 * time.Minute)
	}
	if n := f.Hits("imdb"); n > 3 {
		t.Fatalf("за сутки %d запросов к сбойной записи — нужно не больше 3", n)
	}
	if queueLen(t, r) != 1 {
		t.Fatal("задача должна остаться в очереди")
	}
}

// Кинопоиск лёг (5xx на всё): после трёх таких ответов подряд платный путь встаёт на час —
// иначе очередь за минуты прошла бы по сотне раздач и потратила суточную квоту.
func TestServiceOutagePausesPaidRequests(t *testing.T) {
	f := newFakeKP(t)
	r, _, _ := newRatings(t, f, testKey)
	for i := range 10 {
		enqueue(t, r, i, Item{Release: fmt.Sprintf("rutor:%d", i), IMDbID: fmt.Sprintf("tt%07d", i+1)})
	}
	f.SetStatus(http.StatusBadGateway)
	drain(t, r)
	if n := f.Hits("imdb"); n != 3 {
		t.Fatalf("запросов при лежащем сервисе %d — нужно 3, затем пауза", n)
	}
}

// Один фильм по IMDb — один запрос на все рипы; «Кинопоиск не знает этот IMDb» помнится месяц.
func TestSameIMDbDifferentRipsCostsOneRequest(t *testing.T) {
	f := newFakeKP(t)
	r, _, _ := newRatings(t, f, testKey)
	for i := range 3 {
		enqueue(t, r, i, Item{Release: fmt.Sprintf("rutor:%d", i), IMDbID: "tt0133093"})
	}
	for i := range 2 {
		enqueue(t, r, 10+i, Item{Release: fmt.Sprintf("rutor:1%d", i), IMDbID: "tt0000001", Title: "Неизвестно / Unknown Thing (2020) WEB-DL"})
	}
	drain(t, r)
	for i := range 3 {
		if got, ok := ratingOf(t, r, fmt.Sprintf("rutor:%d", i)); !ok || got.KinopoiskID != 301 {
			t.Fatalf("rutor:%d: %+v, %v", i, got, ok)
		}
	}
	if f.Hits("imdb") != 2 {
		t.Fatalf("запросов по IMDb %d — нужно 2: один на фильм и один на неизвестный IMDb", f.Hits("imdb"))
	}
}

// Номер Кинопоиска из описания пришёл позже (раздача из поиска, описание — при открытии карточки,
// спека, раздел 7) — он главнее: и после «не найдено», и после чужого совпадения по названию.
func TestLaterKinopoiskIDWins(t *testing.T) {
	f := newFakeKP(t)
	f.films[555] = filmJSON(555, "Другой", "Other", 1999, 7.7)
	r, _, _ := newRatings(t, f, testKey)
	enqueue(t, r, 1, Item{Release: "rutor:a", Title: "Неизвестно / Unknown Thing (2020) WEB-DL"})
	enqueue(t, r, 2, Item{Release: "rutor:b", Title: "Матрица / The Matrix (1999) BDRip"})
	drain(t, r)
	enqueue(t, r, 1, Item{Release: "rutor:a", KinopoiskID: 301, Title: "Неизвестно / Unknown Thing (2020) WEB-DL"})
	enqueue(t, r, 2, Item{Release: "rutor:b", KinopoiskID: 555, Title: "Матрица / The Matrix (1999) BDRip"})
	drain(t, r)
	if got, ok := ratingOf(t, r, "rutor:a"); !ok || got.KinopoiskID != 301 {
		t.Fatalf("после «не найдено»: %+v, %v", got, ok)
	}
	if got, ok := ratingOf(t, r, "rutor:b"); !ok || got.KinopoiskID != 555 {
		t.Fatalf("после совпадения по названию: %+v, %v", got, ok)
	}
}

// Каталог обновил задачу, пока она решалась: обновление не теряется.
func TestEnqueueDuringResolveIsKept(t *testing.T) {
	f := newFakeKP(t)
	f.films[555] = filmJSON(555, "Другой", "Other", 1999, 7.7)
	r, _, _ := newRatings(t, f, testKey)
	var once sync.Once
	f.onFilm = func(id int) {
		once.Do(func() {
			if err := r.Enqueue(ctx, 1, Item{Release: "rutor:1", KinopoiskID: 555}); err != nil {
				t.Error(err)
			}
		})
	}
	enqueue(t, r, 1, Item{Release: "rutor:1", KinopoiskID: 301})
	drain(t, r)
	if got, ok := ratingOf(t, r, "rutor:1"); !ok || got.KinopoiskID != 555 {
		t.Fatalf("обновление задачи потерялось: %+v, %v", got, ok)
	}
}

// Каталог обновился: выпавшие из него раздачи уходят в конец очереди, квота — на нынешний топ
// (ревью этапа 5b).
func TestEnqueueCatalogDemotesDropped(t *testing.T) {
	f := newFakeKP(t)
	r, _, _ := newRatings(t, f, testKey)
	enqueue(t, r, 0, Item{Release: "rutor:old", KinopoiskID: 301})
	if err := r.EnqueueCatalog(ctx, []Item{{Release: "rutor:new", IMDbID: "tt0133093"}}); err != nil {
		t.Fatal(err)
	}
	if did, err := r.Step(ctx); !did || err != nil {
		t.Fatal(did, err)
	}
	if f.Hits("imdb") != 1 || f.Hits("film") != 0 {
		t.Fatalf("первой сделана выпавшая раздача: IMDb %d, фильм %d", f.Hits("imdb"), f.Hits("film"))
	}
	if queueLen(t, r) != 1 {
		t.Fatal("выпавшая раздача не удаляется — её мог поставить и поиск")
	}
}

// Ключ сменили в настройках: проблема «ключ не подходит» снимается сразу, лимиты нового ключа
// работающий модуль узнаёт сам, без перезапуска (хвост 5b).
func TestKeyChangeRechecksQuotaWithoutRestart(t *testing.T) {
	f := newFakeKP(t)
	r, _, db := newRatings(t, f, "wrong-key")
	r.now = time.Now
	cctx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- r.Run(cctx) }()
	defer func() { cancel(); <-done }()
	waitFor(t, "проблема «ключ не подходит»", func() bool {
		ps, _ := db.Problems(ctx)
		return len(ps) == 1 && ps[0].ID == ProblemKinopoiskKey
	})
	r.kp.SetKey(testKey)
	r.KeyChanged(ctx)
	if ps, _ := db.Problems(ctx); len(ps) != 0 {
		t.Fatalf("проблема старого ключа осталась: %+v", ps)
	}
	waitFor(t, "лимиты нового ключа", func() bool {
		st, _ := r.Status(ctx)
		return st.HasKey && !st.BadKey && st.Quota.DailyLimit == 500
	})
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("не дождались за 5 с: %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Запись без года совпала обоими названиями, но в карточке фильма год чужой — это не тот фильм:
// рейтинга нет, и другой рип с тем же названием не ищет снова (ревью 5b, M1).
func TestYearlessMatchWithForeignYearIsRejected(t *testing.T) {
	f := newFakeKP(t)
	f.search["The Runner"] = itemsJSON(filmJSON(589920, "Бегущая", "The Runner", 0, nil))
	f.films[589920] = filmJSON(589920, "Бегущая", "The Runner", 2019, 5.0)
	r, _, _ := newRatings(t, f, testKey)
	enqueue(t, r, 1, Item{Release: "rutor:1", Title: "Бегущая / The Runner (2026) WEB-DL 1080p"})
	drain(t, r)
	if got, ok := ratingOf(t, r, "rutor:1"); ok {
		t.Fatalf("чужой фильм: %+v", got)
	}
	enqueue(t, r, 2, Item{Release: "rutor:2", Title: "Бегущая / The Runner (2026) WEB-DLRip"})
	drain(t, r)
	if f.Hits("search") != 1 {
		t.Fatalf("поисков %d — второй рип искал снова", f.Hits("search"))
	}
}
