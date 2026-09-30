package main

import (
	"bytes"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeKinopoisk — фильм 301 по номеру, по IMDb и в рейтинге без ключа; ключ — «k».
func fakeKinopoisk(t *testing.T) *httptest.Server {
	t.Helper()
	film := `{"kinopoiskId":301,"imdbId":"tt0133093","nameRu":"Матрица","nameOriginal":"The Matrix","year":1999,"type":"FILM","ratingKinopoisk":8.5,"ratingImdb":8.7}`
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/301.xml" {
			io.WriteString(w, `<?xml version="1.0" encoding="WINDOWS-1251"?><rating><kp_rating num_vote="1">8.501</kp_rating><imdb_rating num_vote="1">8.7</imdb_rating></rating>`)
			return
		}
		if r.Header.Get("X-API-KEY") != "k" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case r.URL.Path == "/api/v1/api_keys/k":
			io.WriteString(w, `{"totalQuota":{"value":-1,"used":7},"dailyQuota":{"value":500,"used":7},"accountType":"FREE"}`)
		case r.URL.Path == "/api/v2.2/films/301":
			io.WriteString(w, film)
		case r.URL.Path == "/api/v2.2/films" && r.URL.Query().Get("imdbId") == "tt0133093":
			io.WriteString(w, `{"total":1,"totalPages":1,"items":[`+film+`]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func runMeta(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := runCLI(append([]string{"meta"}, args...), &out, &errb)
	return code, out.String(), errb.String()
}

func TestMetaTitle(t *testing.T) {
	code, out, _ := runMeta(t, "title", "Динозавры / The Dinosaurs [S01] (2026) WEB-DL 720p от New-Team")
	if code != 0 || !strings.Contains(out, "Оригинальное: The Dinosaurs") || !strings.Contains(out, "Год:          2026") {
		t.Fatalf("код %d\n%s", code, out)
	}
}

func TestMetaKinopoisk(t *testing.T) {
	s := fakeKinopoisk(t)
	t.Setenv("KINODOM_KP_KEY", "k")
	cases := map[string][]string{
		"За сутки: 7 из 500":                                  {"kp", "quota", "--api", s.URL},
		"301  Матрица / The Matrix (1999) FILM · рейтинг 8.5": {"kp", "film", "--api", s.URL, "301"},
		"Кинопоиск 8.501 · IMDb 8.7":                          {"kp", "rating", "--api", s.URL, "301"},
		"Найдено: Матрица / The Matrix (1999), Кинопоиск 301": {"rate", "--api", s.URL, "--imdb", "tt0133093", "Матрица"},
	}
	for want, args := range cases {
		code, out, errOut := runMeta(t, args...)
		if code != 0 || !strings.Contains(out, want) {
			t.Errorf("%v: код %d\n%s\n%s", args, code, out, errOut)
		}
	}
}

func TestMetaImage(t *testing.T) {
	var pic bytes.Buffer
	png.Encode(&pic, image.NewRGBA(image.Rect(0, 0, 1, 1)))
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(pic.Bytes()) }))
	t.Cleanup(s.Close)
	code, out, errOut := runMeta(t, "image", "--direct", s.URL+"/p.png")
	if code != 0 || !strings.Contains(out, ".png") {
		t.Fatalf("код %d\n%s\n%s", code, out, errOut)
	}
}

func TestMetaUsage(t *testing.T) {
	for _, args := range [][]string{{}, {"title"}, {"kp", "film", "abc"}, {"dance", "x"}} {
		if code, _, errOut := runMeta(t, args...); code != 2 || !strings.Contains(errOut, "Использование") {
			t.Errorf("%v: код %d", args, code)
		}
	}
}

// Кинопоиск без ключа: поиск и карточка через сайт (фейковый GraphQL).
func TestMetaKPWeb(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/graphql/" || r.Header.Get("service-id") != "25" {
			http.NotFound(w, r)
			return
		}
		switch r.URL.Query().Get("operationName") {
		case "SuggestSearch":
			io.WriteString(w, `{"data":{"suggest":{"top":{"topResult":{"global":{"__typename":"Film","id":301,"title":{"russian":"Матрица","original":"The Matrix"},"productionYear":1999,"rating":{"kinopoisk":{"value":8.501}}}},"movies":[]}}}}`)
		case "FilmBaseInfo":
			io.WriteString(w, `{"data":{"film":{"__typename":"Film","id":301,"title":{"russian":"Матрица","original":"The Matrix"},"productionYear":1999,"synopsis":"Жизнь Томаса Андерсона","genres":[{"name":"фантастика"}]}}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(s.Close)
	code, out, errOut := runMeta(t, "kpweb", "search", "--web", s.URL, "Матрица")
	if code != 0 || !strings.Contains(out, "301  Матрица / The Matrix (1999) FILM · рейтинг 8.5") {
		t.Fatalf("поиск: %d %q %q", code, out, errOut)
	}
	code, out, errOut = runMeta(t, "kpweb", "film", "--web", s.URL, "301")
	if code != 0 || !strings.Contains(out, "Жанры: фантастика") || !strings.Contains(out, "Описание: Жизнь Томаса Андерсона") {
		t.Fatalf("карточка: %d %q %q", code, out, errOut)
	}
}
