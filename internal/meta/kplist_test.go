package meta

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/text/encoding/charmap"
)

// План 14Г: список Кинопоиска без ключа — запрос сайта MovieDesktopListPage (текст из белого списка, байт в
// байт), фильтры и порядок — переменными; ответ — фильмы с оценкой, голосами, датой выхода и постером.
func TestKPList(t *testing.T) {
	var got struct {
		OperationName string         `json:"operationName"`
		Variables     map[string]any `json:"variables"`
		Query         string         `json:"query"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("operationName") != "MovieDesktopListPage" || r.Header.Get("service-id") != "25" {
			http.NotFound(w, r)
			return
		}
		json.NewDecoder(r.Body).Decode(&got)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(sample(t, "kp_list_series.json")))
	}))
	defer srv.Close()
	w := NewKPWeb(KPWebOptions{GraphQL: srv.URL + "/graphql/", Site: srv.URL, Every: time.Millisecond})
	items, total, err := w.List(context.Background(), KPBackground, ListQuery{Slug: "popular-series", Bool: []string{"russian", "foreign"},
		Order: "POSITION_ASC", Limit: 50, Offset: 100})
	if err != nil || total != 1000 || len(items) != 3 {
		t.Fatalf("список: %d из %d, %v", len(items), total, err)
	}
	want, _ := os.ReadFile("kpgql/MovieDesktopListPage.graphql")
	if got.Query != string(want) || got.Variables["slug"] != "popular-series" || got.Variables["moviesOrder"] != "POSITION_ASC" ||
		num(got.Variables["moviesLimit"]) != 50 || num(got.Variables["moviesOffset"]) != 100 {
		t.Fatalf("переменные: %v", got.Variables)
	}
	fs, _ := got.Variables["filters"].(map[string]any)
	bs, _ := fs["booleanFilterValues"].([]any)
	if len(bs) != 2 || bs[0].(map[string]any)["filterId"] != "foreign" || bs[1].(map[string]any)["filterId"] != "russian" {
		t.Fatalf("логические фильтры (по алфавиту): %v", fs)
	}
	f := items[0]
	if f.ID != 7036356 || f.Type != "TV_SERIES" || f.NameRu != "Холод" || f.Year != 2026 || f.Rating != 7.664 || f.Votes != 233305 ||
		!f.Premiere.Equal(time.Date(2026, 7, 16, 0, 0, 0, 0, time.UTC)) ||
		f.Poster != "https://avatars.mds.yandex.net/get-kinopoisk-image/4486454/87387feb-9961-49d5-bee7-c867bef417a0/300x450" ||
		!slices.Equal(f.Genres, []string{"драма", "триллер"}) || !slices.Equal(f.Countries, []string{"Россия"}) {
		t.Fatalf("первый: %+v", f)
	}
}

// Документальные — жанр выбором одного значения.
func TestKPListGenre(t *testing.T) {
	var got struct {
		Variables map[string]any `json:"variables"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got)
		w.Write([]byte(sample(t, "kp_list_series.json")))
	}))
	defer srv.Close()
	w := NewKPWeb(KPWebOptions{GraphQL: srv.URL + "/graphql/", Site: srv.URL, Every: time.Millisecond})
	if _, _, err := w.List(context.Background(), KPBackground, ListQuery{Genre: "documentary", Order: "VOTES_COUNT_DESC", Limit: 50}); err != nil {
		t.Fatal(err)
	}
	fs, _ := got.Variables["filters"].(map[string]any)
	ss, _ := fs["singleSelectFilterValues"].([]any)
	if got.Variables["slug"] != "" || len(ss) != 1 || ss[0].(map[string]any)["filterId"] != "genre" || ss[0].(map[string]any)["value"] != "documentary" {
		t.Fatalf("жанр: %v", got.Variables)
	}
}

// Оценка IMDb — rating.kinopoisk.ru/{id}.xml в cp1251; у фильма без IMDb — 0 без ошибки; ответ не 200 —
// ошибка.
func TestKPIMDb(t *testing.T) {
	enc := func(s string) []byte { b, _ := charmap.Windows1251.NewEncoder().Bytes([]byte(s)); return b }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/301.xml":
			w.Write(enc(`<?xml version="1.0" encoding="windows-1251" standalone="yes"?><rating><kp_rating num_vote="814782">8.501</kp_rating><imdb_rating num_vote="2200000">8.7</imdb_rating></rating>`))
		case "/5.xml":
			w.Write(enc(`<?xml version="1.0" encoding="windows-1251"?><rating><kp_rating num_vote="12">6.1</kp_rating></rating>`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	w := NewKPWeb(KPWebOptions{RatingBase: srv.URL, Every: time.Millisecond, RatingEvery: time.Millisecond})
	if r, v, err := w.IMDb(context.Background(), 301); err != nil || r != 8.7 || v != 2200000 {
		t.Fatalf("301: %v %v %v", r, v, err)
	}
	if r, v, err := w.IMDb(context.Background(), 5); err != nil || r != 0 || v != 0 {
		t.Fatalf("без IMDb: %v %v %v", r, v, err)
	}
	if _, _, err := w.IMDb(context.Background(), 7); err == nil || errors.Is(err, ErrKPBlocked) || strings.Contains(err.Error(), srv.URL) {
		t.Fatalf("404: %v", err)
	}
}

// Ревью 14Г, Important 1: сайт больше не принимает запрос списка — пауза этому запросу (повтор не идёт на
// сайт), ErrKPBlocked; пустой список — сбой, а не «пусто».
func TestKPListNotAllowedPauses(t *testing.T) {
	var hits atomic.Int32
	body := "not"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if body == "null" {
			w.Write([]byte(`{"data":{"movieListBySlug":null}}`))
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(sample(t, "kpweb-not-allowed.json")))
	}))
	defer srv.Close()
	w := NewKPWeb(KPWebOptions{GraphQL: srv.URL + "/graphql/", Site: srv.URL, Every: time.Millisecond})
	for i := range 3 {
		if _, _, err := w.List(context.Background(), KPList, ListQuery{Slug: "popular-films", Limit: 50}); !errors.Is(err, ErrKPBlocked) {
			t.Fatalf("попытка %d: %v", i+1, err)
		}
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("запросов к сайту %d, нужен 1 (дальше — пауза)", n)
	}
	body = "null"
	w2 := NewKPWeb(KPWebOptions{GraphQL: srv.URL + "/graphql/", Site: srv.URL, Every: time.Millisecond})
	if _, _, err := w2.List(context.Background(), KPList, ListQuery{Slug: "popular-films", Limit: 50}); err == nil {
		t.Fatal("пустой список — нужна ошибка")
	}
}

// Ревью 14Г, Important 1: каталог «Кинопоиск» (KPList) не берёт резерв суточного предела — он для правки и
// медиатеки; ждёт, пока идут запросы каталога и правки, как фоновая очередь.
func TestKPListNoReserve(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(sample(t, "kp_list_series.json")))
	}))
	defer srv.Close()
	w := NewKPWeb(KPWebOptions{GraphQL: srv.URL + "/graphql/", Site: srv.URL, Every: time.Millisecond, DailyLimit: 5, Reserve: 2})
	for i := range 3 {
		if _, _, err := w.List(context.Background(), KPList, ListQuery{Limit: 50}); err != nil {
			t.Fatalf("запрос %d: %v", i+1, err)
		}
	}
	if _, _, err := w.List(context.Background(), KPList, ListQuery{Limit: 50}); !errors.Is(err, ErrKPDailyLimit) {
		t.Fatalf("резерв: %v", err)
	}
	if _, _, err := w.List(context.Background(), KPBackground, ListQuery{Limit: 50}); err != nil {
		t.Fatalf("медиатеке резерв доступен: %v", err)
	}
}

// Ревью 14Г, Important 2: капча у сервиса оценок (переход на страницу или HTML вместо XML) — пауза оценкам и
// ErrKPBlocked, а не «у фильма нет IMDb»; следующий запрос на сервис не идёт.
func TestKPIMDbCaptcha(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		switch r.URL.Path {
		case "/1.xml":
			http.Redirect(w, r, "/showcaptcha?retpath=x", http.StatusFound)
		case "/2.xml":
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte("<html><body>Подтвердите, что вы не робот</body></html>"))
		default:
			w.Write([]byte("<html>captcha</html>"))
		}
	}))
	defer srv.Close()
	for _, id := range []int{1, 2} {
		hits.Store(0)
		w := NewKPWeb(KPWebOptions{RatingBase: srv.URL, Every: time.Millisecond, RatingEvery: time.Millisecond})
		if _, _, err := w.IMDb(context.Background(), id); !errors.Is(err, ErrKPBlocked) {
			t.Fatalf("%d: %v", id, err)
		}
		if _, _, err := w.IMDb(context.Background(), 3); !errors.Is(err, ErrKPBlocked) {
			t.Fatalf("%d, после капчи: %v", id, err)
		}
		if n := hits.Load(); n != 1 {
			t.Fatalf("%d: запросов %d — после капчи пауза", id, n)
		}
	}
}

// Ревью 14Г: образец списка фильмов с настоящего сайта — разбор фильма (productionYear, премьера), а не только
// сериала (releaseYears).
func TestKPListFilms(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(sample(t, "kp_list_films.json")))
	}))
	defer srv.Close()
	w := NewKPWeb(KPWebOptions{GraphQL: srv.URL + "/graphql/", Site: srv.URL, Every: time.Millisecond})
	items, _, err := w.List(context.Background(), KPBackground, ListQuery{Slug: "popular-films", Bool: []string{"foreign"}, Order: "POSITION_ASC", Limit: 3})
	if err != nil || len(items) == 0 {
		t.Fatalf("список: %d, %v", len(items), err)
	}
	dated := 0
	for _, f := range items {
		if f.Type != "FILM" || f.Year < 1900 || f.NameRu == "" || f.Poster == "" {
			t.Fatalf("фильм: %+v", f)
		}
		if !f.Premiere.IsZero() {
			dated++
		}
	}
	if dated == 0 {
		t.Fatalf("ни у одного фильма нет даты выхода: %+v", items)
	}
}

// Ревью 15В, Important 2: пауза оценок IMDb (капча, 403/429) — строка в журнале у самих ворот оценок, одна на
// паузу: каталог о паузах ворот молчит.
func TestKPIMDbPauseLogged(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	var buf bytes.Buffer
	w := NewKPWeb(KPWebOptions{RatingBase: srv.URL, Every: time.Millisecond, RatingEvery: time.Millisecond,
		Log: slog.New(slog.NewTextHandler(&buf, nil))})
	for id := range 3 {
		if _, _, err := w.IMDb(context.Background(), id+1); !errors.Is(err, ErrKPBlocked) {
			t.Fatalf("%d: %v", id+1, err)
		}
	}
	if n := strings.Count(buf.String(), "оценки IMDb: пауза"); n != 1 {
		t.Fatalf("строк о паузе %d: %s", n, buf.String())
	}
}

// Ревью 15В, Important 2: суточный предел исчерпан — строка в журнале, одна в сутки.
func TestKPDailyLimitLoggedOnce(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(sample(t, "kp_list_series.json")))
	}))
	defer srv.Close()
	var buf bytes.Buffer
	w := NewKPWeb(KPWebOptions{GraphQL: srv.URL + "/graphql/", Site: srv.URL, Every: time.Millisecond, DailyLimit: 2, Reserve: -1,
		Log: slog.New(slog.NewTextHandler(&buf, nil))})
	for range 4 {
		w.List(context.Background(), KPList, ListQuery{Limit: 50})
	}
	if n := strings.Count(buf.String(), "суточный предел"); n != 1 {
		t.Fatalf("строк о пределе %d: %s", n, buf.String())
	}
}
