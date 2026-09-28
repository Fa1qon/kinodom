package meta

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

var ctx = context.Background()

func TestQuota(t *testing.T) {
	f := newFakeKP(t)
	q, err := newKP(f, testKey).Quota(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if q.DailyLimit != 500 || q.DailyUsed != 3 || q.TotalLimit != -1 || q.Account != "FREE" {
		t.Fatalf("лимиты %+v", q)
	}
}

func TestFilmAndIMDb(t *testing.T) {
	f := newFakeKP(t)
	kp := newKP(f, testKey)
	want := Film{ID: 301, IMDbID: "tt0133093", NameRu: "Матрица", NameOrig: "The Matrix", Year: 1999, Type: "FILM", Rating: 8.5, RatingIMDb: 8.7}
	got, err := kp.Film(ctx, 301)
	if err != nil || got != want {
		t.Fatalf("фильм %+v, %v", got, err)
	}
	got, err = kp.ByIMDb(ctx, "tt0133093")
	if err != nil || got != want {
		t.Fatalf("по IMDb %+v, %v", got, err)
	}
	if _, err := kp.ByIMDb(ctx, "tt0000001"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("нет в IMDb: %v", err)
	}
	if _, err := kp.Film(ctx, 7); !errors.Is(err, ErrNotFound) {
		t.Fatalf("нет фильма: %v", err)
	}
}

// Поиск: год ±1, порядок по числу оценок, все фильмы; null в рейтинге и годе — нули, не ошибка.
func TestSearch(t *testing.T) {
	f := newFakeKP(t)
	fs, err := newKP(f, testKey).Search(ctx, "The Matrix", 1999)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"keyword=The+Matrix", "yearFrom=1998", "yearTo=2000", "order=NUM_VOTE", "type=ALL"} {
		if !strings.Contains(f.Last(), want) {
			t.Errorf("в запросе %q нет %q", f.Last(), want)
		}
	}
	if len(fs) != 5 || fs[0].ID != 301 || fs[0].Rating != 8.5 || fs[2].Rating != 0 || fs[3].NameRu != "" || fs[4].Year != 0 {
		t.Fatalf("найдено %+v", fs)
	}
}

func TestErrorCodes(t *testing.T) {
	cases := map[int]error{
		http.StatusUnauthorized:    ErrBadKey,
		http.StatusPaymentRequired: ErrQuota,
		http.StatusNotFound:        ErrNotFound,
		http.StatusTooManyRequests: ErrRateLimited,
	}
	for code, want := range cases {
		f := newFakeKP(t)
		f.SetStatus(code)
		if _, err := newKP(f, testKey).Film(ctx, 301); !errors.Is(err, want) {
			t.Errorf("ответ %d: %v, нужно %v", code, err, want)
		}
	}
	f := newFakeKP(t)
	f.SetStatus(http.StatusBadGateway)
	var se *ServiceError
	if _, err := newKP(f, testKey).Film(ctx, 301); !errors.As(err, &se) || se.Status != 502 {
		t.Fatalf("ответ 502: %v", err)
	}
}

// Кириллица в поиске — 500 (исследование, раздел 12): сбой сервиса, а не «не найдено».
func TestCyrillicSearchIsServiceError(t *testing.T) {
	f := newFakeKP(t)
	var se *ServiceError
	if _, err := newKP(f, testKey).Search(ctx, "Матрица", 1999); !errors.As(err, &se) || se.Status != 500 {
		t.Fatalf("получено %v", err)
	}
}

// Ключ не попадает в текст ошибки, даже когда он в адресе (/api/v1/api_keys/{key}).
func TestErrorsHideKey(t *testing.T) {
	f := newFakeKP(t)
	f.Close() // сервера больше нет — ошибка сети с адресом внутри
	secret := "secret-key-12345"
	kp := NewKinopoisk(KinopoiskOptions{Key: secret, APIBase: f.URL, RatingBase: f.URL, Rate: 1000})
	_, err := kp.Quota(ctx)
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("ошибка %v", err)
	}
}

// Без ключа: API не трогаем, рейтинг по номеру — без ключа (rating.kinopoisk.ru).
func TestWithoutKey(t *testing.T) {
	f := newFakeKP(t)
	kp := newKP(f, "")
	if _, err := kp.Film(ctx, 301); !errors.Is(err, ErrNoKey) {
		t.Fatalf("фильм без ключа: %v", err)
	}
	if _, err := kp.Quota(ctx); !errors.Is(err, ErrNoKey) {
		t.Fatalf("лимиты без ключа: %v", err)
	}
	if f.Hits("film")+f.Hits("key") != 0 {
		t.Fatal("без ключа на API ходить незачем")
	}
	r, imdb, err := kp.KeylessRating(ctx, 301)
	if err != nil || r != 8.501 || imdb != 8.7 {
		t.Fatalf("рейтинг без ключа %v %v, %v", r, imdb, err)
	}
	if _, _, err := kp.KeylessRating(ctx, 7); !errors.Is(err, ErrNotFound) {
		t.Fatalf("рейтинг без ключа, нет фильма: %v", err)
	}
}

func TestPosterURL(t *testing.T) {
	kp := NewKinopoisk(KinopoiskOptions{})
	if got := kp.PosterURL(301); got != "https://kinopoiskapiunofficial.tech/images/posters/kp/301.jpg" {
		t.Fatalf("постер %s", got)
	}
}
