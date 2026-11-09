package library

import (
	"context"
	"slices"
	"testing"

	"kinodom/internal/meta"
)

// fakeCatalog — каталог пакета для тестов.
type fakeCatalog struct {
	films   []meta.Film
	details map[int]meta.FilmDetails
}

func (c *fakeCatalog) EachTitle(_ context.Context, fn func(meta.Film)) error {
	for _, f := range c.films {
		fn(f)
	}
	return nil
}

func (c *fakeCatalog) Details(_ context.Context, id int) (meta.FilmDetails, bool) {
	d, ok := c.details[id]
	return d, ok
}

func dune() *fakeCatalog {
	return &fakeCatalog{
		films: []meta.Film{film(2882, "Дюна", "Dune", 1984, "FILM"), film(409424, "Дюна", "Dune: Part One", 2021, "FILM"),
			film(229087, "Дюна", "Dune", 2000, "TV_SERIES")},
		details: map[int]meta.FilmDetails{409424: {Film: film(409424, "Дюна", "Dune: Part One", 2021, "FILM"),
			Description: "desc-dune", Genres: []string{"фантастика"}, PosterURL: "http://kp/dune.jpg"}},
	}
}

// Без Кинопоиска фильм из папки опознаётся по каталогу пакета (название и год), карточка — из каталога;
// без года каталог не угадывает — единица ждёт Кинопоиск.
func TestCatalogWithoutKP(t *testing.T) {
	e := newEnv(t)
	e.l.o.KP = nil
	cat := dune()
	e.l.o.Catalog = func() Catalog { return cat }
	e.folder(t, catFilms, "Movies", "Дюна.2021.1080p.mkv", "Дюна.mkv")
	e.scan(t)
	v := e.list(t, "pc", 0)
	var got *CardSummary
	for i, c := range v.Cards {
		if c.Key == "kp-409424" {
			got = &v.Cards[i]
		}
	}
	if got == nil || got.Title != "Дюна" || got.Year != 2021 || got.Poster != "/img/"+meta.ImageKey("http://kp/dune.jpg") {
		t.Fatalf("карточки: %+v", v.Cards)
	}
	card, err := e.l.Card(ctx, "pc", "kp-409424")
	if err != nil || card.Description != "desc-dune" || !slices.Equal(card.Genres, []string{"фантастика"}) {
		t.Errorf("карточка: %+v (%v)", card, err)
	}
	var states []string
	rows, _ := e.d.R.Query(`SELECT state FROM lib_units ORDER BY name`)
	for rows.Next() {
		var s string
		rows.Scan(&s)
		states = append(states, s)
	}
	rows.Close()
	if !slices.Equal(states, []string{StateFound, StateNew}) {
		t.Errorf("состояния: %v", states)
	}
}

// С Кинопоиском фильм из каталога в него не ходит; фильма нет в каталоге — поиск на Кинопоиске.
func TestCatalogBeforeKP(t *testing.T) {
	e := newEnv(t)
	crimeKP(e)
	cat := dune()
	e.l.o.Catalog = func() Catalog { return cat }
	e.folder(t, catFilms, "Movies", "Дюна.2021.1080p.mkv", "Crime.101.2026.WEBRip.mkv")
	e.scan(t)
	if keys := keysOf(e.list(t, "pc", 0).Cards); keys != "kp-10 kp-409424" && keys != "kp-409424 kp-10" {
		t.Fatalf("карточки: %s", keys)
	}
	for _, c := range e.kp.calls {
		if c == "Dune" || c == "Дюна" {
			t.Errorf("Кинопоиск спрашивали о фильме из каталога: %q", e.kp.calls)
		}
	}
	if !slices.Contains(e.kp.calls, "Crime 101") {
		t.Errorf("фильм не из каталога не искали: %q", e.kp.calls)
	}
}

// «Не распознано» — с подсказками из каталога: точные названия любого года, фильмы раньше сериала,
// затем названия со всеми словами; одна подсказка — номер для «Привязать».
func TestUnrecognizedSuggestions(t *testing.T) {
	e := newEnv(t)
	cat := dune()
	cat.films = append(cat.films, film(4540126, "Дюна: Часть вторая", "Dune: Part Two", 2024, "FILM"))
	e.l.o.Catalog = func() Catalog { return cat }
	e.folder(t, catFilms, "Movies", "Дюна.mkv")
	e.scan(t)
	us, err := e.l.Unrecognized(ctx, "pc")
	if err != nil || len(us) != 1 {
		t.Fatalf("нераспознанные: %+v (%v)", us, err)
	}
	var got []int
	for _, s := range us[0].Suggestions {
		got = append(got, s.KP)
	}
	if !slices.Equal(got, []int{409424, 2882, 229087, 4540126}) {
		t.Errorf("подсказки: %+v", us[0].Suggestions)
	}
}
