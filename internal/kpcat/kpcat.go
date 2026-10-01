// Package kpcat — каталог «Кинопоиск» (план 14Г): популярные фильмы и сериалы сайта по разделам (русские и
// зарубежные, документальные) в четырёх порядках — популярность, оценка Кинопоиска, оценка IMDb, новизна.
// Списки — запросом сайта без ключа раз в сутки; IMDb — по фильму с сервиса оценок, с кэшем на 30 дней.
// Раздачи фильма пульт ищет поиском (Rutor, Rutracker и источник поиска — Jacred).
package kpcat

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"kinodom/internal/meta"
	"kinodom/internal/store"
	"kinodom/internal/supervisor"
)

// Lister — Кинопоиск без ключа (meta.KPWeb): списки и оценка IMDb.
type Lister interface {
	List(ctx context.Context, class meta.KPClass, q meta.ListQuery) ([]meta.ListFilm, int, error)
	IMDb(ctx context.Context, id int) (float64, int, error)
}

type Options struct {
	DB      *store.DB
	KP      Lister
	Posters func(ctx context.Context, src string) (string, error) // постер в кэш картинок: ключ /img/{key}
	Every   time.Duration                                         // 0 — раз в сутки
	Log     *slog.Logger
	Now     func() time.Time
}

// section — раздел каталога: список сайта и сколько из него брать.
type section struct {
	id, name string
	q        meta.ListQuery
	max      int
}

// sections — разделы (решение заказчика 2026-10-01): популярные наборы сайта; документальных популярных нет —
// по числу голосов, вышедшие.
var sections = []section{
	{"films-ru", "Фильмы русские", meta.ListQuery{Slug: "popular-films", Bool: []string{"russian"}, Order: "POSITION_ASC"}, 1000},
	{"films-foreign", "Фильмы зарубежные", meta.ListQuery{Slug: "popular-films", Bool: []string{"foreign"}, Order: "POSITION_ASC"}, 1000},
	{"series-ru", "Сериалы русские", meta.ListQuery{Slug: "popular-series", Bool: []string{"russian"}, Order: "POSITION_ASC"}, 1000},
	{"series-foreign", "Сериалы зарубежные", meta.ListQuery{Slug: "popular-series", Bool: []string{"foreign"}, Order: "POSITION_ASC"}, 1000},
	{"docs", "Документальные", meta.ListQuery{Bool: []string{"released"}, Genre: "documentary", Order: "VOTES_COUNT_DESC"}, 500},
}

// orders — порядки разделов.
var orders = []struct{ id, name string }{
	{"popular", "Популярные"}, {"kp", "Оценка КП"}, {"imdb", "Оценка IMDb"}, {"new", "Новые"},
}

// pageLimit — фильмов за запрос (сайт больше 50 не отдаёт).
const pageLimit = 50

// imdbFor — сколько живёт оценка IMDb (и «у сайта нет»).
const imdbFor = 30 * 24 * time.Hour

// Module — модуль «kpcat» под сторожем.
type Module struct {
	o   Options
	st  db
	log *slog.Logger
	now func() time.Time
}

func New(o Options) *Module {
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Every == 0 {
		o.Every = 24 * time.Hour
	}
	return &Module{o: o, st: db{o.DB}, log: o.Log, now: o.Now}
}

func (m *Module) Name() string { return "kpcat" }

// Run — обновление разделов раз в Every, между ними — оценки IMDb порциями. Отказ Кинопоиска — повтор позже.
func (m *Module) Run(ctx context.Context) error {
	supervisor.Ready(ctx)
	timer := time.NewTimer(time.Minute) // после запуска — не сразу: каталог трекеров и медиатека первыми
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
		}
		wait := time.Hour
		if due, err := m.st.refreshDue(ctx, m.now().Add(-m.o.Every)); err == nil && due {
			if err := m.Refresh(ctx); err != nil && ctx.Err() == nil {
				m.log.Info("каталог Кинопоиска: обновление не удалось", "err", err)
				wait = 30 * time.Minute
			}
		}
		if did, err := m.fillIMDb(ctx, 20); err != nil && ctx.Err() == nil {
			m.log.Info("каталог Кинопоиска: оценки IMDb не пришли", "err", err)
			wait = 30 * time.Minute
		} else if did > 0 {
			wait = time.Second
		}
		timer.Reset(wait)
	}
}

// Refresh — все разделы: раздел целиком в память, потом места заменяются одной транзакцией; отказ сайта
// посреди раздела — раздел не трогается (Review Focus 1), остальные обновляются.
func (m *Module) Refresh(ctx context.Context) error {
	var errs []error
	for _, s := range sections {
		var all []meta.ListFilm
		var err error
		for offset := 0; offset < s.max; offset += pageLimit {
			q := s.q
			q.Limit, q.Offset = min(pageLimit, s.max-offset), offset
			var page []meta.ListFilm
			var total int
			if page, total, err = m.o.KP.List(ctx, meta.KPBackground, q); err != nil {
				break
			}
			all = append(all, page...)
			if len(page) == 0 || offset+len(page) >= total {
				break
			}
		}
		if err != nil {
			errs = append(errs, err)
			if ctx.Err() != nil || errors.Is(err, meta.ErrKPBlocked) {
				break
			}
			continue
		}
		if err := m.st.replaceSection(ctx, s.id, all, m.now()); err != nil {
			return err
		}
	}
	return errors.Join(errs...)
}

// fillIMDb — оценки IMDb до n фильмов, которых не спрашивали (или спрашивали дольше 30 дней): сначала ближе
// к началу разделов. did — сколько спросили.
func (m *Module) fillIMDb(ctx context.Context, n int) (did int, err error) {
	ids, err := m.st.imdbDue(ctx, m.now().Add(-imdbFor), n)
	if err != nil {
		return 0, err
	}
	for _, id := range ids {
		r, v, err := m.o.KP.IMDb(ctx, id)
		if err != nil {
			return did, err
		}
		if err := m.st.saveIMDb(ctx, id, r, v, m.now()); err != nil {
			return did, err
		}
		did++
	}
	return did, nil
}

// known — раздел и порядок есть.
func known(sec string) bool {
	for _, s := range sections {
		if s.id == sec {
			return true
		}
	}
	return false
}

func orderOf(o string) string {
	for _, x := range orders {
		if x.id == o {
			return o
		}
	}
	return "popular"
}

// list — фильмы раздела в порядке order со смещения offset, не больше limit; total — всего в разделе.
func (m *Module) list(ctx context.Context, sec, order string, offset, limit int) ([]FilmView, int, error) {
	rows, total, err := m.st.list(ctx, sec, orderOf(order), offset, limit)
	if err != nil {
		return nil, 0, err
	}
	out := make([]FilmView, len(rows))
	for i, r := range rows {
		out[i] = r.view()
	}
	return out, total, nil
}
