package meta

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeKPWeb — сайт Кинопоиска: GraphQL принимает только тексты запросов из kpgql байт в байт (как
// белый список сайта) и только с заголовком service-id: 25; страницы фильма — с cookie без SSO.
type fakeKPWeb struct {
	*httptest.Server
	t *testing.T

	mu         sync.Mutex
	suggest    map[string]string // ключевое слово → файл testdata
	film       map[int]string
	series     map[int]string
	pages      map[string]string // путь страницы → файл testdata
	notAllowed map[string]bool   // операция → «the query is not allowed»
	status     int               // ≠ 0 — ответ с этим кодом на всё
	body       string            // ≠ "" — этот ответ (text/html) на всё
	calls      []kpWebCall
}

type kpWebCall struct {
	op  string // операция GraphQL или путь страницы
	key string // ключевое слово или номер
	at  time.Time
}

func newFakeKPWeb(t *testing.T) *fakeKPWeb {
	f := &fakeKPWeb{t: t, suggest: map[string]string{}, film: map[int]string{}, series: map[int]string{},
		pages: map[string]string{}, notAllowed: map[string]bool{}}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeKPWeb) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.URL.Path != "/graphql/" {
		f.calls = append(f.calls, kpWebCall{op: r.URL.Path, at: time.Now()})
		if c, err := r.Cookie("disable_server_sso_redirect"); err != nil || c.Value != "1" {
			http.Redirect(w, r, "https://sso.example/auth", http.StatusFound)
			return
		}
		if f.answerForced(w) {
			return
		}
		name, ok := f.pages[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(sample(f.t, name)))
		return
	}
	var req struct {
		OperationName string         `json:"operationName"`
		Variables     map[string]any `json:"variables"`
		Query         string         `json:"query"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	key := ""
	switch req.OperationName {
	case "SuggestSearch":
		key, _ = req.Variables["keyword"].(string)
	case "FilmBaseInfo":
		key = strconv.Itoa(int(num(req.Variables["filmId"])))
	case "TvSeriesBaseInfo":
		key = strconv.Itoa(int(num(req.Variables["tvSeriesId"])))
	}
	f.calls = append(f.calls, kpWebCall{op: req.OperationName, key: key, at: time.Now()})
	if r.Header.Get("service-id") != "25" {
		http.NotFound(w, r)
		return
	}
	if f.answerForced(w) {
		return
	}
	want, err := os.ReadFile("kpgql/" + req.OperationName + ".graphql")
	if err != nil || req.Query != string(want) || r.URL.Query().Get("operationName") != req.OperationName || f.notAllowed[req.OperationName] {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(sample(f.t, "kpweb-not-allowed.json")))
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	id, _ := strconv.Atoi(key)
	var name string
	switch req.OperationName {
	case "SuggestSearch":
		name = f.suggest[key]
		if name == "" {
			w.Write([]byte(`{"data":{"suggest":{"top":{"topResult":null,"movies":[],"__typename":"SuggestTop"}}}}`))
			return
		}
	case "FilmBaseInfo":
		name = f.film[id]
		if name == "" {
			w.Write([]byte(`{"data":{"film":null,"tvSeries":null},"errors":[{"message":"There is no film with id ` + key + `","extensions":{"code":"NotFoundError"}}]}`))
			return
		}
	case "TvSeriesBaseInfo":
		name = f.series[id]
		if name == "" {
			w.Write([]byte(`{"data":{"tvSeries":null},"errors":[{"message":"There is no TV series with id ` + key + `","extensions":{"code":"NotFoundError"}}]}`))
			return
		}
	}
	w.Write([]byte(sample(f.t, name)))
}

func (f *fakeKPWeb) answerForced(w http.ResponseWriter) bool {
	switch {
	case f.status != 0:
		w.WriteHeader(f.status)
		return true
	case f.body != "":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(f.body))
		return true
	}
	return false
}

func num(v any) float64 { n, _ := v.(float64); return n }

func (f *fakeKPWeb) set(fn func()) { f.mu.Lock(); fn(); f.mu.Unlock() }

func (f *fakeKPWeb) Calls() []kpWebCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]kpWebCall(nil), f.calls...)
}

func newKPWebFor(f *fakeKPWeb, clk *clock, mod func(*KPWebOptions)) *KPWeb {
	o := KPWebOptions{GraphQL: f.URL + "/graphql/", Site: f.URL, Every: time.Millisecond, Now: clk.now}
	if mod != nil {
		mod(&o)
	}
	return NewKPWeb(o)
}

func findFilm(fs []Film, id int) (Film, bool) {
	for _, f := range fs {
		if f.ID == id {
			return f, true
		}
	}
	return Film{}, false
}

// Поиск сайта (исследование 22.4): вид, годы, рейтинг, оригинальное название; текст запроса — как у сайта.
func TestKPWebSuggest(t *testing.T) {
	f := newFakeKPWeb(t)
	f.suggest["Удар"] = "kpweb-suggest-udar.json"
	f.suggest["Трудно быть богом"] = "kpweb-suggest-trudno.json"
	clk := &clock{t: time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local)}
	w := newKPWebFor(f, clk, nil)
	fs, err := w.Suggest(ctx, KPNormal, "Удар")
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) == 0 || fs[0].ID != 8174380 || fs[0].Type != "TV_SERIES" || fs[0].NameRu != "Удар" || fs[0].Year != 2025 || fs[0].YearEnd != 0 {
		t.Fatalf("первый — сериал 2025: %+v", fs)
	}
	if m, ok := findFilm(fs, 12541899); !ok || m.Type != "FILM" || m.Year != 2026 || m.NameOrig != "La frappe" {
		t.Fatalf("фильм 2026: %+v", m)
	}
	if m, ok := findFilm(fs, 705287); !ok || m.Rating != 7.369 || m.Year != 2014 {
		t.Fatalf("фильм 2014 с рейтингом: %+v", m)
	}
	fs, err = w.Suggest(ctx, KPNormal, "Трудно быть богом")
	if err != nil {
		t.Fatal(err)
	}
	if m, ok := findFilm(fs, 7954692); !ok || m.Type != "TV_SERIES" || m.Year != 2026 || m.Rating != 6.716 {
		t.Fatalf("сериал 2026: %+v", m)
	}
	if m, ok := findFilm(fs, 40783); !ok || m.Type != "FILM" || m.Year != 2013 {
		t.Fatalf("фильм 2013: %+v", m)
	}
	if fs, err := w.Suggest(ctx, KPNormal, "Нет такого"); err != nil || len(fs) != 0 {
		t.Fatalf("пустой поиск: %v %v", fs, err)
	}
}

// Карточка фильма и сериала: описание, жанры, рейтинги, постер; частичная ошибка (tvSeries у фильма) — не ошибка.
func TestKPWebDetails(t *testing.T) {
	f := newFakeKPWeb(t)
	f.film[301] = "kpweb-film-301.json"
	f.series[7036356] = "kpweb-series-7036356.json"
	clk := &clock{t: time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local)}
	w := newKPWebFor(f, clk, nil)
	d, err := w.Details(ctx, KPNormal, 301, false)
	if err != nil {
		t.Fatal(err)
	}
	if d.ID != 301 || d.NameRu != "Матрица" || d.NameOrig != "The Matrix" || d.Year != 1999 || d.Type != "FILM" ||
		d.Rating != 8.501 || d.RatingIMDb != 8.7 || !strings.HasPrefix(d.Description, "Жизнь Томаса Андерсона") ||
		strings.Join(d.Genres, ",") != "фантастика,боевик" ||
		d.PosterURL != "https://avatars.mds.yandex.net/get-kinopoisk-image/4774061/cf1970bc-3f08-4e0e-a095-2fb57c3aa7c6/600x900" {
		t.Fatalf("фильм: %+v", d)
	}
	d, err = w.Details(ctx, KPNormal, 7036356, true)
	if err != nil {
		t.Fatal(err)
	}
	if d.Type != "TV_SERIES" || d.NameRu != "Холод" || d.Year != 2026 || d.YearEnd != 2026 || !strings.HasPrefix(d.Description, "Женя попадает") ||
		strings.Join(d.Genres, ",") != "драма,триллер" || !strings.HasSuffix(d.PosterURL, "/600x900") {
		t.Fatalf("сериал: %+v", d)
	}
	if _, err := w.Details(ctx, KPNormal, 999, false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("нет такого фильма: %v", err)
	}
}

// Текст запроса карточки устарел («the query is not allowed») — запасной путь: страница с JSON-LD; поиск не страдает.
func TestKPWebPageFallback(t *testing.T) {
	f := newFakeKPWeb(t)
	f.notAllowed["TvSeriesBaseInfo"] = true
	f.pages["/series/5325705/"] = "kpweb-page-series-5325705.html"
	f.suggest["Удар"] = "kpweb-suggest-udar.json"
	clk := &clock{t: time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local)}
	w := newKPWebFor(f, clk, nil)
	d, err := w.Details(ctx, KPNormal, 5325705, true)
	if err != nil {
		t.Fatal(err)
	}
	if d.ID != 5325705 || d.NameRu != "Законник" || d.Type != "TV_SERIES" || d.Year != 2023 || d.Rating != 7.969 ||
		!strings.HasPrefix(d.Description, "Жизнь Кирилла") || strings.Join(d.Genres, ",") != "детектив,драма" ||
		!strings.HasPrefix(d.PosterURL, "https://avatars.mds.yandex.net/") {
		t.Fatalf("со страницы: %+v", d)
	}
	n := len(f.Calls())
	if _, err := w.Details(ctx, KPNormal, 5325705, true); err != nil {
		t.Fatal(err)
	}
	if calls := f.Calls()[n:]; len(calls) != 1 || calls[0].op != "/series/5325705/" {
		t.Fatalf("устаревший запрос не повторяется до конца паузы — сразу страница: %+v", calls)
	}
	if _, err := w.Suggest(ctx, KPNormal, "Удар"); err != nil {
		t.Fatalf("поиск работает: %v", err)
	}
}

// Отказ (403, 429, капча, «the query is not allowed» у поиска) — пауза 6 ч без запросов, причина в Status, потом снова.
func TestKPWebBlockedPauses(t *testing.T) {
	cases := []struct {
		name string
		set  func(f *fakeKPWeb)
	}{
		{"403", func(f *fakeKPWeb) { f.status = http.StatusForbidden }},
		{"429", func(f *fakeKPWeb) { f.status = http.StatusTooManyRequests }},
		{"капча", func(f *fakeKPWeb) {
			f.body = `<html><body><form action="/checkcaptcha">SmartCaptcha</form></body></html>`
		}},
		{"запрос не пускают", func(f *fakeKPWeb) { f.notAllowed["SuggestSearch"] = true }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFakeKPWeb(t)
			f.suggest["Удар"] = "kpweb-suggest-udar.json"
			clk := &clock{t: time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local)}
			w := newKPWebFor(f, clk, nil)
			f.set(func() { c.set(f) })
			if _, err := w.Suggest(ctx, KPNormal, "Удар"); !errors.Is(err, ErrKPBlocked) {
				t.Fatalf("отказ: %v", err)
			}
			n := len(f.Calls())
			if _, err := w.Suggest(ctx, KPNormal, "Удар"); !errors.Is(err, ErrKPBlocked) || len(f.Calls()) != n {
				t.Fatalf("во время паузы — без запросов: %v, запросов %d → %d", err, n, len(f.Calls()))
			}
			st := w.Status()
			if !st.PausedUntil.Equal(clk.now().Add(6*time.Hour)) || st.Reason == "" {
				t.Fatalf("состояние: %+v", st)
			}
			if strings.Contains(st.Reason, f.URL) {
				t.Fatalf("адрес в причине: %q", st.Reason)
			}
			f.set(func() { f.status, f.body, f.notAllowed = 0, "", map[string]bool{} })
			clk.add(6*time.Hour + time.Second)
			if _, err := w.Suggest(ctx, KPNormal, "Удар"); err != nil {
				t.Fatalf("после паузы: %v", err)
			}
			if st := w.Status(); !st.PausedUntil.IsZero() || st.Reason != "" {
				t.Fatalf("пауза не снялась: %+v", st)
			}
		})
	}
}

// 5xx — обычная ошибка без паузы; в тексте нет адреса.
func TestKPWebServerErrorNoPause(t *testing.T) {
	f := newFakeKPWeb(t)
	f.status = http.StatusBadGateway
	clk := &clock{t: time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local)}
	w := newKPWebFor(f, clk, nil)
	_, err := w.Suggest(ctx, KPNormal, "Удар")
	if err == nil || errors.Is(err, ErrKPBlocked) || strings.Contains(err.Error(), f.URL) {
		t.Fatalf("5xx: %v", err)
	}
	if !w.Status().PausedUntil.IsZero() {
		t.Fatal("5xx — без паузы")
	}
}

// Суточный предел: сверх него — ErrKPDailyLimit без запроса; назавтра — снова.
func TestKPWebDailyLimit(t *testing.T) {
	f := newFakeKPWeb(t)
	f.suggest["Удар"] = "kpweb-suggest-udar.json"
	clk := &clock{t: time.Date(2026, 9, 30, 23, 0, 0, 0, time.Local)}
	w := newKPWebFor(f, clk, func(o *KPWebOptions) { o.DailyLimit = 2 })
	for range 2 {
		if _, err := w.Suggest(ctx, KPNormal, "Удар"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := w.Suggest(ctx, KPNormal, "Удар"); !errors.Is(err, ErrKPDailyLimit) || len(f.Calls()) != 2 {
		t.Fatalf("сверх предела: %v, запросов %d", err, len(f.Calls()))
	}
	if st := w.Status(); st.Today != 2 {
		t.Fatalf("за сутки: %+v", st)
	}
	clk.add(2 * time.Hour)
	if _, err := w.Suggest(ctx, KPNormal, "Удар"); err != nil {
		t.Fatalf("назавтра: %v", err)
	}
}

// Не чаще одного запроса в Every — вместе GraphQL и страницы.
func TestKPWebEvery(t *testing.T) {
	f := newFakeKPWeb(t)
	f.suggest["Удар"] = "kpweb-suggest-udar.json"
	f.notAllowed["FilmBaseInfo"] = true
	f.pages["/film/301/"] = "kpweb-page-series-5325705.html"
	clk := &clock{t: time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local)}
	w := newKPWebFor(f, clk, func(o *KPWebOptions) { o.Every = 150 * time.Millisecond })
	w.Suggest(ctx, KPNormal, "Удар")
	w.Details(ctx, KPNormal, 301, false) // GraphQL, потом страница
	calls := f.Calls()
	if len(calls) != 3 {
		t.Fatalf("запросы: %+v", calls)
	}
	for i := 1; i < len(calls); i++ {
		if gap := calls[i].at.Sub(calls[i-1].at); gap < 140*time.Millisecond {
			t.Fatalf("запрос %d через %v после предыдущего", i, gap)
		}
	}
}

// Медиатека (KPBackground) ждёт, пока есть ждущие запросы каталога и открытой раздачи (Review Focus 4).
func TestKPWebBackgroundWaitsForNormal(t *testing.T) {
	f := newFakeKPWeb(t)
	for _, k := range []string{"первый", "фон", "срочный"} {
		f.suggest[k] = "kpweb-suggest-udar.json"
	}
	clk := &clock{t: time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local)}
	w := newKPWebFor(f, clk, func(o *KPWebOptions) { o.Every = 200 * time.Millisecond })
	if _, err := w.Suggest(ctx, KPNormal, "первый"); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Go(func() { w.Suggest(ctx, KPBackground, "фон") })
	time.Sleep(30 * time.Millisecond)
	wg.Go(func() { w.Suggest(ctx, KPNormal, "срочный") })
	wg.Wait()
	var order []string
	for _, c := range f.Calls() {
		order = append(order, c.key)
	}
	if strings.Join(order, ",") != "первый,срочный,фон" {
		t.Fatalf("порядок: %v", order)
	}
}
