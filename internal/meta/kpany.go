package meta

import (
	"context"
	"errors"
	"sync"
)

// KPAny — Кинопоиск для медиатеки (спека 11b, 5.6): сначала без токена (сайт, очередь KPBackground —
// после каталога и открытого), а когда сайт на паузе или исчерпан суточный предел — ключ, если задан.
// Types — вид фильма по номеру из своей базы ("" — неизвестен): карточке сериала нужен другой запрос.
type KPAny struct {
	Web   *KPWeb
	Key   *Kinopoisk
	Types func(ctx context.Context, id int) (string, error)

	mu   sync.Mutex
	seen map[int]string // номер → вид из последних поисков
}

// webDown — сайт сейчас не ответит: отказ или суточный предел.
func webDown(err error) bool { return errors.Is(err, ErrKPBlocked) || errors.Is(err, ErrKPDailyLimit) }

// Search — фильмы по названию; год в запрос сайта не входит (его сверяет медиатека).
func (a *KPAny) Search(ctx context.Context, keyword string, year int) ([]Film, error) {
	if a.Web != nil {
		fs, err := a.Web.Suggest(ctx, KPBackground, keyword)
		if err == nil {
			a.mu.Lock()
			if a.seen == nil {
				a.seen = map[int]string{}
			}
			for _, f := range fs {
				a.seen[f.ID] = f.Type
			}
			a.mu.Unlock()
			return fs, nil
		}
		if !webDown(err) || a.Key == nil || !a.Key.HasKey() {
			return nil, err
		}
	}
	if a.Key == nil {
		return nil, ErrNoKey
	}
	return a.Key.Search(ctx, keyword, year)
}

// Details — описание, жанры, постер по номеру. Вид неизвестен — сначала фильм, «нет такого» — сериал.
func (a *KPAny) Details(ctx context.Context, id int) (FilmDetails, error) {
	if a.Web != nil {
		typ := a.typeOf(ctx, id)
		d, err := a.Web.Details(ctx, KPBackground, id, seriesTypes[typ])
		if errors.Is(err, ErrNotFound) && typ == "" {
			d, err = a.Web.Details(ctx, KPBackground, id, true)
		}
		if err == nil || !webDown(err) || a.Key == nil || !a.Key.HasKey() {
			return d, err
		}
	}
	if a.Key == nil {
		return FilmDetails{}, ErrNoKey
	}
	return a.Key.Details(ctx, id)
}

func (a *KPAny) typeOf(ctx context.Context, id int) string {
	a.mu.Lock()
	typ := a.seen[id]
	a.mu.Unlock()
	if typ == "" && a.Types != nil {
		if t, err := a.Types(ctx, id); err == nil {
			typ = t
		}
	}
	return typ
}
