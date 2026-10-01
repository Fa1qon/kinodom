package meta

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
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
