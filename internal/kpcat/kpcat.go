// Package kpcat — каталог «Кинопоиск» (план 14Г): популярные фильмы и сериалы сайта по разделам (русские и
// зарубежные, документальные) в четырёх порядках — популярность, оценка Кинопоиска, оценка IMDb, новизна.
// Списки — запросом сайта без ключа раз в сутки; IMDb — по фильму с сервиса оценок, с кэшем на 30 дней.
// Раздачи фильма пульт ищет поиском (Rutor, Rutracker и источник поиска — Jacred).
package kpcat

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sync"
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
	// FirstDelay — первое обновление после запуска; 0 — через минуту (каталог трекеров и медиатека первыми).
	FirstDelay time.Duration
	Log        *slog.Logger
	Now        func() time.Time
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

	mu   sync.Mutex
	errs map[string]string // раздел → почему не удалась последняя попытка обновления (ревью 14Г: пустой раздел
	// иначе вечно «идёт первое обновление»)
	snaps map[string]*snapshot // токен → снимок порядка
}

// snapshot — порядок раздела, снятый первой порцией: следующие порции листают его (ревью 14Г: по смещению в
// живом порядке фильмы повторялись и пропадали, когда приходили оценки IMDb или суточное обновление).
type snapshot struct {
	sec, order string
	ids        []int
	used       time.Time
}

const (
	snapFor = time.Hour // снимок живёт после последнего обращения
	snapMax = 64        // снимков всего; лишний — давний
)

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
	return &Module{o: o, st: db{o.DB}, log: o.Log, now: o.Now, errs: map[string]string{}, snaps: map[string]*snapshot{}}
}

func (m *Module) Name() string { return "kpcat" }

// Run — обновление разделов раз в Every, между ними — оценки IMDb порциями. Отказ Кинопоиска — повтор позже.
func (m *Module) Run(ctx context.Context) error {
	supervisor.Ready(ctx)
	first := m.o.FirstDelay
	if first == 0 {
		first = time.Minute
	}
	timer := time.NewTimer(first)
	defer timer.Stop()
	// Свои сроки у обновления и у оценок (ревью 14Г): сбой обновления — не раньше чем через 30 мин, и оценки,
	// пришедшие в ту же итерацию, этот срок не сбивают — иначе неудачный раздел выбирал бы суточный предел.
	var refreshAt, imdbAt time.Time
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
		}
		now := m.now()
		if !now.Before(refreshAt) {
			refreshAt = now.Add(time.Hour)
			if err := m.refreshDue(ctx); err != nil && ctx.Err() == nil {
				if !paused(err) {
					m.log.Info("каталог Кинопоиска: обновление не удалось", "err", err)
				}
				refreshAt = now.Add(30 * time.Minute)
			}
		}
		if !now.Before(imdbAt) {
			imdbAt = now.Add(time.Hour)
			if did, err := m.fillIMDb(ctx, 20); err != nil && ctx.Err() == nil {
				if !paused(err) {
					m.log.Info("каталог Кинопоиска: оценки IMDb не пришли", "err", err)
				}
				imdbAt = now.Add(30 * time.Minute)
			} else if did > 0 {
				imdbAt = now.Add(time.Second)
			}
		}
		next := refreshAt
		if imdbAt.Before(next) {
			next = imdbAt
		}
		timer.Reset(max(time.Second, next.Sub(m.now())))
	}
}

// paused — все ошибки прохода — отказы ворот Кинопоиска (пауза или суточный предел): строку о них пишут сами
// ворота — каталог её не повторяет на каждую попытку (ревью 14Г). Есть другая ошибка — не пауза: в журнал
// (ревью 15В, Important 2).
func paused(err error) bool {
	if j, ok := err.(interface{ Unwrap() []error }); ok {
		for _, e := range j.Unwrap() {
			if !paused(e) {
				return false
			}
		}
		return len(j.Unwrap()) > 0
	}
	return errors.Is(err, meta.ErrKPBlocked) || errors.Is(err, meta.ErrKPDailyLimit)
}

// shortSection — сайт обещал больше, чем отдал (пустая страница посреди раздела).
type shortSection struct {
	name      string
	got, want int
}

func (e shortSection) Error() string {
	return fmt.Sprintf("Кинопоиск: в разделе «%s» пришло %d из %d", e.name, e.got, e.want)
}

// humanErr — почему раздел не обновился, для пульта: суточный предел и «пришло N из M» — как есть, остальное
// (пауза, сеть, ответы сайта) — «Кинопоиск не отвечает»; подробности — в журнале (ревью 15В, Important 1: в
// пульт шёл сырой текст сетевой ошибки по-английски, с адресом).
func humanErr(err error) string {
	var short shortSection
	switch {
	case errors.Is(err, meta.ErrKPDailyLimit):
		return meta.ErrKPDailyLimit.Error()
	case errors.As(err, &short):
		return short.Error()
	}
	return meta.ErrKPBlocked.Error()
}

// sectionErr — почему не удалась последняя попытка обновить раздел; "" — удалась или не было.
func (m *Module) sectionErr(sec string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.errs[sec]
}

func (m *Module) setSectionErr(sec string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err == nil {
		delete(m.errs, sec)
	} else {
		m.errs[sec] = humanErr(err)
	}
}

// refreshDue — разделы, которые не обновляли дольше Every (или ни разу).
func (m *Module) refreshDue(ctx context.Context) error {
	fresh, err := m.st.freshSections(ctx, m.now().Add(-m.o.Every))
	if err != nil {
		return err
	}
	var due []section
	for _, s := range sections {
		if !fresh[s.id] {
			due = append(due, s)
		}
	}
	return m.refresh(ctx, due)
}

// Refresh — все разделы: раздел целиком в память, потом места заменяются одной транзакцией; отказ сайта
// посреди раздела — раздел не трогается (Review Focus 1), остальные обновляются.
func (m *Module) Refresh(ctx context.Context) error { return m.refresh(ctx, sections) }

func (m *Module) refresh(ctx context.Context, secs []section) error {
	var errs []error
	for i, s := range secs {
		var all []meta.ListFilm
		var err error
		total := 0
		for offset := 0; offset < s.max; offset += pageLimit {
			q := s.q
			q.Limit, q.Offset = min(pageLimit, s.max-offset), offset
			var page []meta.ListFilm
			if page, total, err = m.o.KP.List(ctx, meta.KPList, q); err != nil {
				break
			}
			all = append(all, page...)
			if len(page) == 0 || offset+len(page) >= total {
				break
			}
		}
		if err == nil && len(all)*10 < min(total, s.max)*9 {
			// Сайт обещал больше, чем отдал (пустая страница посреди раздела) — раздел не обрезается.
			err = shortSection{s.name, len(all), min(total, s.max)}
		}
		if err != nil {
			errs = append(errs, err)
			if ctx.Err() != nil {
				break
			}
			m.setSectionErr(s.id, err)
			if errors.Is(err, meta.ErrKPBlocked) {
				// Пауза ворот: остальные разделы не спрашивались — у них та же причина (Review Focus 2, 15В).
				for _, rest := range secs[i+1:] {
					m.setSectionErr(rest.id, err)
				}
				break
			}
			continue
		}
		if err := m.st.replaceSection(ctx, s.id, all, m.now()); err != nil {
			return err
		}
		m.setSectionErr(s.id, nil)
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
			if ctx.Err() != nil || errors.Is(err, meta.ErrKPBlocked) {
				return did, err
			}
			// Сбой по одному фильму — повтор через сутки, очередь идёт дальше (ревью 14Г).
			if err := m.st.saveIMDbRetry(ctx, id, m.now().Add(-imdbFor+24*time.Hour)); err != nil {
				return did, err
			}
			did++
			continue
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

// list — фильмы раздела в живом порядке order со смещения offset, не больше limit; total — всего в разделе. Тот
// же запрос, что у снимков порядка (orderIDs), — тесты порядков проверяют его (ревью 15В, Minor 2).
func (m *Module) list(ctx context.Context, sec, order string, offset, limit int) ([]FilmView, int, error) {
	ids, err := m.st.orderIDs(ctx, sec, orderOf(order), m.now())
	if err != nil {
		return nil, 0, err
	}
	from := min(offset, len(ids))
	to := min(from+limit, len(ids))
	fs, err := m.st.filmsByIDs(ctx, ids[from:to])
	if err != nil {
		return nil, 0, err
	}
	out := make([]FilmView, len(fs))
	for i, f := range fs {
		out[i] = f.view()
	}
	return out, len(ids), nil
}

// page — порция раздела из снимка порядка token; нет его (первая порция, снимок устарел, сервер перезапущен) —
// из живого порядка под новым снимком (Review Focus 1, 15В). total — фильмов в снимке.
func (m *Module) page(ctx context.Context, sec, order, token string, offset, limit int) (out []FilmView, total int, snap string, err error) {
	order = orderOf(order)
	now := m.now()
	m.mu.Lock()
	s := m.snaps[token]
	if s != nil && offset > 0 && s.sec == sec && s.order == order {
		s.used = now
	} else {
		s = nil
	}
	m.mu.Unlock()
	if s == nil {
		ids, err := m.st.orderIDs(ctx, sec, order, now)
		if err != nil {
			return nil, 0, "", err
		}
		var b [8]byte
		rand.Read(b[:])
		token = hex.EncodeToString(b[:])
		s = &snapshot{sec: sec, order: order, ids: ids, used: now}
		m.mu.Lock()
		m.dropSnaps(now)
		m.snaps[token] = s
		m.mu.Unlock()
	}
	from := min(offset, len(s.ids))
	to := min(from+limit, len(s.ids))
	fs, err := m.st.filmsByIDs(ctx, s.ids[from:to])
	if err != nil {
		return nil, 0, "", err
	}
	out = make([]FilmView, len(fs))
	for i, f := range fs {
		out[i] = f.view()
	}
	return out, len(s.ids), token, nil
}

// dropSnaps — снимки, к которым не обращались snapFor, и лишние сверх snapMax (давние первыми). Под m.mu.
func (m *Module) dropSnaps(now time.Time) {
	for k, s := range m.snaps {
		if now.Sub(s.used) > snapFor {
			delete(m.snaps, k)
		}
	}
	for len(m.snaps) >= snapMax {
		old := ""
		for k, s := range m.snaps {
			if old == "" || s.used.Before(m.snaps[old].used) {
				old = k
			}
		}
		delete(m.snaps, old)
	}
}

// ImageKeys — ключи постеров каталога в кэше картинок: чистка кэша их не удаляет (ревью 14Г).
func (m *Module) ImageKeys(ctx context.Context) (map[string]bool, error) {
	ps, err := m.st.posters(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(ps))
	for _, p := range ps {
		out[meta.ImageKey(p)] = true
	}
	return out, nil
}
