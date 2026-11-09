package library

import (
	"context"
	"slices"
	"strings"

	"kinodom/internal/meta"
)

// Catalog — каталог пакета рекомендаций (recs.Model): опознание единиц без сети и карточки найденного
// (docs/data-packs-and-updates.md, раздел 4). Кинопоиск нужен, только когда каталог фильма не знает.
type Catalog interface {
	// EachTitle — все фильмы каталога: номер Кинопоиска, названия, год, тип (FILM или TV_SERIES).
	EachTitle(ctx context.Context, fn func(meta.Film)) error
	// Details — карточка фильма каталога; false — фильма нет или у него нет описания.
	Details(ctx context.Context, id int) (meta.FilmDetails, bool)
}

// catIndex — фильмы каталога по нормализованному названию (русскому и оригинальному).
type catIndex struct {
	src    Catalog
	byName map[string][]meta.Film
}

// catalog — индекс текущего каталога; пакет сменился — индекс строится заново. nil — каталога нет.
func (l *Library) catalog(ctx context.Context) *catIndex {
	if l.o.Catalog == nil {
		return nil
	}
	src := l.o.Catalog()
	if src == nil {
		return nil
	}
	l.mu.Lock()
	idx := l.catIdx
	l.mu.Unlock()
	if idx != nil && idx.src == src {
		return idx
	}
	idx = &catIndex{src: src, byName: map[string][]meta.Film{}}
	err := src.EachTitle(ctx, func(f meta.Film) {
		for _, name := range []string{f.NameRu, f.NameOrig} {
			if n := Norm(name); n != "" && !hasFilm(idx.byName[n], f.ID) {
				idx.byName[n] = append(idx.byName[n], f)
			}
		}
	})
	if err != nil {
		l.log.Warn("медиатека: каталог пакета не прочитался", "err", err)
		return nil
	}
	l.mu.Lock()
	l.catIdx = idx
	l.mu.Unlock()
	return idx
}

// lookup — фильмы каталога с одним из названий targets.
func (c *catIndex) lookup(targets map[string]bool) []meta.Film {
	var out []meta.Film
	for n := range targets {
		for _, f := range c.byName[n] {
			if !hasFilm(out, f.ID) {
				out = append(out, f)
			}
		}
	}
	return out
}

func hasFilm(fs []meta.Film, id int) bool {
	for _, f := range fs {
		if f.ID == id {
			return true
		}
	}
	return false
}

// Suggestion — фильм каталога, похожий на нераспознанную единицу: привязать одним нажатием.
type Suggestion struct {
	KP        int    `json:"kp"`
	Title     string `json:"title"`
	TitleOrig string `json:"titleOrig,omitempty"`
	Year      int    `json:"year,omitempty"`
	Series    bool   `json:"series,omitempty"`
}

// maxSuggestions — подсказок у одной единицы.
const maxSuggestions = 5

// suggest — до пяти фильмов каталога для единицы: сначала точные названия (любой год), затем названия,
// где есть все слова разобранного, и названия из двух и больше слов, целиком входящие в разобранное
// («Матрица Перезагрузка Extended»; однословные — только началом: «Дюна Director Cut»); выше — подходящий устройству тип и
// близкий год.
func (c *catIndex) suggest(title string, year int, layout Layout) []Suggestion {
	targets := map[string]bool{}
	for _, v := range Variants(title) {
		if n := Norm(v); n != "" {
			targets[n] = true
		}
	}
	if len(targets) == 0 {
		return nil
	}
	exact := c.lookup(targets)
	var loose []meta.Film
	if len(exact) < maxSuggestions {
		for n, fs := range c.byName {
			if targets[n] {
				continue
			}
			for t := range targets {
				if wordsIn(t, n) || strings.HasPrefix(t, n+" ") || (strings.Contains(n, " ") && wordsIn(n, t)) {
					for _, f := range fs {
						if !hasFilm(exact, f.ID) && !hasFilm(loose, f.ID) {
							loose = append(loose, f)
						}
					}
					break
				}
			}
		}
	}
	score := func(f meta.Film) int {
		s := 0
		if (layout == LayoutSeries) == seriesTypes[f.Type] {
			s -= 1000
		}
		if year > 0 {
			s += min(abs(f.Year-year), 100)
		}
		return s
	}
	sortBy := func(fs []meta.Film) {
		slices.SortStableFunc(fs, func(a, b meta.Film) int {
			if d := score(a) - score(b); d != 0 {
				return d
			}
			return b.Year - a.Year
		})
	}
	sortBy(exact)
	sortBy(loose)
	var out []Suggestion
	for _, f := range append(exact, loose...) {
		if len(out) == maxSuggestions {
			break
		}
		out = append(out, Suggestion{KP: f.ID, Title: f.NameRu, TitleOrig: f.NameOrig, Year: f.Year, Series: seriesTypes[f.Type]})
	}
	return out
}

// wordsIn — все слова a есть среди слов b (b длиннее).
func wordsIn(a, b string) bool {
	wa, wb := strings.Fields(a), strings.Fields(b)
	if len(wa) == 0 || len(wa) >= len(wb) {
		return false
	}
	for _, w := range wa {
		if !slices.Contains(wb, w) {
			return false
		}
	}
	return true
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
