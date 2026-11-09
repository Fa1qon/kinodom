package library

import (
	"context"

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
