package meta

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
)

// Описание, жанры и постер фильма — для карточек медиатеки (спека этапа 9, раздел 5.4).
func TestFilmDetails(t *testing.T) {
	f := newFakeKP(t)
	kp := newKP(f, testKey)
	d, err := kp.Details(context.Background(), 301)
	if err != nil {
		t.Fatal(err)
	}
	if d.ID != 301 || d.NameRu != "Матрица" || d.NameOrig != "The Matrix" || d.Year != 1999 || d.Type != "FILM" || d.Rating != 8.5 ||
		!strings.HasPrefix(d.Description, "Жизнь Томаса Андерсона") || !slices.Equal(d.Genres, []string{"фантастика", "боевик"}) ||
		d.PosterURL != "https://kinopoiskapiunofficial.tech/images/posters/kp/301.jpg" {
		t.Errorf("фильм: %+v", d)
	}
	if _, err := kp.Details(context.Background(), 999); !errors.Is(err, ErrNotFound) {
		t.Errorf("нет такого: %v", err)
	}
	f.SetStatus(http.StatusPaymentRequired)
	if _, err := kp.Details(context.Background(), 301); !errors.Is(err, ErrQuota) {
		t.Errorf("квота: %v", err)
	}
}

// Рейтинги по номерам — для карточек медиатеки; AddFilm не затирает рейтинг нулём.
func TestRatingsFilms(t *testing.T) {
	f := newFakeKP(t)
	r, _, _ := newRatings(t, f, testKey)
	ctx := context.Background()
	if err := r.AddFilm(ctx, Film{ID: 5, NameRu: "Пять", Year: 2020, Type: "FILM", Rating: 7.1, RatingIMDb: 6.9}); err != nil {
		t.Fatal(err)
	}
	if err := r.AddFilm(ctx, Film{ID: 5, NameRu: "Пять!", Year: 2020, Type: "FILM"}); err != nil {
		t.Fatal(err)
	}
	got, err := r.Films(ctx, []int{5, 6})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[5].Kinopoisk != 7.1 || got[5].IMDb != 6.9 || got[5].NameRu != "Пять!" || got[5].KinopoiskID != 5 {
		t.Errorf("рейтинги: %+v", got)
	}
}
