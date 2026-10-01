package catalog

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"kinodom/internal/source"
)

// Поиск (спека, раздел 7).
const (
	searchCacheFor = 30 * time.Minute // кэш одинаковых запросов
	// searchErrorGrace — законченный с ошибкой поиск ещё столько отдаётся опросам клиента (он
	// спрашивает раз в секунду, пока Complete = false), потом тот же запрос ищет заново.
	searchErrorGrace = 10 * time.Second
)

// searchLimit — общий предел на поиск (тесты укорачивают).
var searchLimit = 30 * time.Second

// Состояния трекера в поиске; иначе — текст ошибки.
const (
	SearchOK      = "ok"
	SearchRunning = "идёт"
)

// SearchState — что найдено к этому моменту. Клиент повторяет запрос раз в секунду, пока
// Complete = false (спека, раздел 7).
type SearchState struct {
	Query    string
	Results  []Entry           // объединённые, одинаковый infohash схлопнут, по раздающим
	Complete bool              // все трекеры ответили, не ответили или вышло время
	Trackers map[string]string // трекер → SearchOK, SearchRunning или текст ошибки
}

// searchRun — идущий или законченный поиск.
type searchRun struct {
	started  time.Time
	sources  string // кто искал (searchSources): другой набор — кэш не годится
	mu       sync.Mutex
	ids      []int64 // найденные раздачи в базе
	status   map[string]string
	done     bool
	finished time.Time
}

// Search запускает поиск (или берёт идущий, или законченный не раньше 30 минут назад без
// ошибок) и сразу отвечает тем, что уже есть. Найденное сохраняется в базе: карточку можно
// открыть (этап 7).
func (c *Catalog) Search(ctx context.Context, query string) (SearchState, error) {
	return c.search(ctx, query, false)
}

// search — Search; poll — повторный опрос того же поиска (законченный с ошибкой трекера не ищет заново).
func (c *Catalog) search(ctx context.Context, query string, poll bool) (SearchState, error) {
	run, q, err := c.startSearch(query, poll, true)
	if err != nil {
		return SearchState{}, err
	}
	return c.searchState(ctx, q, run)
}

// startSearch запускает поиск или берёт идущий (законченный без ошибок — не раньше 30 минут назад).
// poll — повторный опрос того же поиска: законченный с ошибкой трекера поиск он не запускает заново.
// Возвращает и запрос без лишних пробелов. enrich — найденное без страницы — в срочную догрузку
// (поиск из строки поиска; у «Искать на трекерах» — своя, только тот же фильм).
func (c *Catalog) startSearch(query string, poll, enrich bool) (*searchRun, string, error) {
	q := strings.Join(strings.Fields(query), " ")
	if q == "" {
		return nil, "", errors.New("пустой поисковый запрос")
	}
	key := strings.ToLower(q)
	now := c.now()
	c.mu.Lock()
	for k, s := range c.searches {
		if now.Sub(s.started) > searchCacheFor {
			delete(c.searches, k)
		}
	}
	names := c.searchSources()
	sig := strings.Join(names, ",")
	run, ok := c.searches[key]
	if ok && !poll && run.staleWithErrors(now) {
		ok = false // поиск с ошибками не кэшируется: новый запрос ищет заново, повторный опрос — нет
	}
	if ok && !poll && run.sources != sig {
		ok = false // включили или выключили трекер или источник поиска
	}
	if !ok {
		run = &searchRun{started: now, sources: sig, status: map[string]string{}}
		for _, name := range names {
			run.status[name] = SearchRunning
		}
		c.searches[key] = run
		parent := c.runCtx
		if parent == nil {
			parent = context.Background()
		}
		sctx, cancel := context.WithTimeout(parent, searchLimit)
		go c.runSearch(sctx, cancel, run, q, enrich)
	}
	c.mu.Unlock()
	return run, q, nil
}

// runSearch — все трекеры одновременно; каждый результат — в базу и в поиск сразу, как пришёл.
func (c *Catalog) runSearch(ctx context.Context, cancel context.CancelFunc, run *searchRun, q string, enrich bool) {
	defer cancel()
	var wg sync.WaitGroup
	run.mu.Lock()
	names := slices.Collect(maps.Keys(run.status))
	run.mu.Unlock()
	for _, name := range names {
		search := func(ctx context.Context, q string) ([]source.Release, error) { return c.sources[name].Search(ctx, q) }
		extra := c.extra != nil && name == c.extra.Name()
		if extra {
			search = c.extra.Search
		}
		wg.Go(func() {
			rs, err := search(ctx, q)
			var partial *source.PartialError
			switch {
			case errors.Is(err, context.DeadlineExceeded) && len(rs) == 0:
				err = errors.New("не ответил за 30 с")
			case errors.Is(err, context.Canceled) && len(rs) == 0:
				err = errors.New("каталог остановлен") // модуль остановили посреди поиска (ревью 5c)
			case err != nil && !errors.As(err, &partial):
				rs = nil
			}
			var ids []int64
			var serr error
			if extra {
				ids, serr = c.st.saveExtra(context.WithoutCancel(ctx), rs, c.Own, c.ownWithAddress, c.now())
			} else {
				ids, serr = c.st.saveFound(context.WithoutCancel(ctx), rs, c.now())
			}
			switch {
			case serr != nil:
				c.log.Error("поиск: найденное не записалось", "err", serr)
				err = errors.Join(err, serr)
			case extra:
				c.extraFound(context.WithoutCancel(ctx), rs, ids, enrich)
			case enrich:
				c.enqueueFound(context.WithoutCancel(ctx), name, ids, searchToEnrich)
			}
			run.mu.Lock()
			run.ids = append(run.ids, ids...)
			run.status[name] = SearchOK
			if err != nil {
				run.status[name] = err.Error()
			}
			run.mu.Unlock()
		})
	}
	wg.Wait()
	run.mu.Lock()
	run.done, run.finished = true, c.now()
	run.mu.Unlock()
}

// searchToEnrich — сколько найденных без страницы догружать вне очереди на трекер: постеры первого
// экрана поиска (замечание № 10 этапа 11b).
const searchToEnrich = 20

// enqueueFound — найденное трекером name без страницы раздачи — в догрузку вне очереди (после
// открытых в пульте) в порядке выдачи, не больше limit.
// Трекер выключен или форум на паузе — нет: догрузка всё равно не пойдёт.
func (c *Catalog) enqueueFound(ctx context.Context, name string, ids []int64, limit int) {
	if !c.configured(name) || c.forumPausedUntil(name).After(c.now()) {
		return
	}
	byID, err := c.st.rowsByID(ctx, ids)
	if err != nil {
		c.log.Warn("поиск: найденное не читается", "err", err)
		return
	}
	var batch []int64
	now := c.now()
	for _, id := range ids {
		// Страница не загрузилась (не «трекер лежит») — ждёт паузу повтора: пульт спрашивает карточки раз в
		// 3 с, и без неё трекеру уходил бы запрос той же страницы каждые 3 с (финальное ревью 11b-Ж).
		if r, ok := byID[id]; ok && r.DetailsAt.IsZero() && !r.RetryAt.After(now) {
			if batch = append(batch, id); len(batch) == limit {
				break
			}
		}
	}
	if len(batch) > 0 {
		c.findSoon(name, batch)
	}
}

// extraFound — найденное источником поиска: темы своих трекеров — в догрузку вне очереди (поиск из строки
// поиска), раздачи без страницы — в очередь рейтингов первыми: номер Кинопоиска по названию даёт постер и
// рейтинг (спека 11b, раздел 8).
func (c *Catalog) extraFound(ctx context.Context, rs []source.Release, ids []int64, enrich bool) {
	own := map[string][]int64{}
	var pageless []int64
	for i, r := range rs {
		if c.ownWithAddress(r.Tracker) {
			own[r.Tracker] = append(own[r.Tracker], ids[i])
		} else {
			pageless = append(pageless, ids[i])
		}
	}
	if enrich {
		for tracker, ids := range own {
			c.enqueueFound(ctx, tracker, ids, searchToEnrich)
		}
	}
	c.kinopoiskSoon(ctx, pageless)
}

// kinopoiskSoon — раздачи без страницы без номера Кинопоиска — в очередь рейтингов первыми; у кого нет
// картинки, ждут номер для постера (ratingResolved).
func (c *Catalog) kinopoiskSoon(ctx context.Context, ids []int64) {
	if c.ratings == nil || len(ids) == 0 {
		return
	}
	byID, err := c.st.rowsByID(ctx, ids)
	if err != nil {
		c.log.Warn("поиск: найденное не читается", "err", err)
		return
	}
	noImage := map[string]int64{}
	var keys []string
	queued := 0
	for _, id := range ids {
		r, ok := byID[id]
		if !ok || r.KinopoiskID != 0 {
			continue
		}
		// В очередь рейтингов — первые searchToEnrich (квота Кинопоиска без токена), а постер по уже
		// известному номеру — всем: экран сортирует по раздающим, источник — нет (финальное ревью 11b-Д).
		if queued < searchToEnrich {
			queued++
			if err := c.ratings.Enqueue(ctx, 0, ratingItem(r)); err != nil {
				c.log.Warn("поиск: раздача не встала в очередь рейтингов", "err", err)
				return
			}
		}
		if key := r.Tracker + ":" + r.TopicID; r.ImageKey == "" {
			noImage[key] = id
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return
	}
	// Номер мог найтись сразу, без очереди (соседняя раздача того же фильма): постер Кинопоиска — сейчас,
	// срочным путём; нет — раздача ждёт номер (ratingResolved).
	known, err := c.ratings.For(ctx, keys)
	if err != nil {
		c.log.Warn("поиск: рейтинги не читаются", "err", err)
		return
	}
	for _, key := range keys {
		if kp := known[key].KinopoiskID; kp > 0 {
			c.posterLater(ctx, noImage[key], "", kp, posterSoon)
			continue
		}
		c.mu.Lock()
		if len(c.kpWait) >= kpWaitLimit {
			clear(c.kpWait)
		}
		c.kpWait[key] = noImage[key]
		c.mu.Unlock()
	}
}

// staleWithErrors — поиск закончился с ошибкой трекера и опросы клиента уже получили итог.
func (s *searchRun) staleWithErrors(now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.done || now.Sub(s.finished) < searchErrorGrace {
		return false
	}
	for _, st := range s.status {
		if st != SearchOK {
			return true
		}
	}
	return false
}

func (c *Catalog) searchState(ctx context.Context, q string, run *searchRun) (SearchState, error) {
	rs, st, err := c.runRows(ctx, run)
	if err != nil {
		return SearchState{}, err
	}
	rs = c.collapse(rs)
	preferFirst(rs, c.PreferredFormat()) // формат в приоритете — первым (спека этапа 7, раздел 10.3)
	if st.Results, err = c.entries(ctx, rs); err != nil {
		return SearchState{}, err
	}
	st.Query = q
	return st, nil
}

// runRows — найденное к этому моменту: раздачи без повторов в порядке прихода, состояние трекеров и
// «поиск закончен» (Query и Results не заполнены).
func (c *Catalog) runRows(ctx context.Context, run *searchRun) ([]row, SearchState, error) {
	run.mu.Lock()
	ids := append([]int64(nil), run.ids...)
	status := make(map[string]string, len(run.status))
	for k, v := range run.status {
		status[k] = v
	}
	done := run.done
	run.mu.Unlock()
	byID, err := c.st.rowsByID(ctx, ids)
	if err != nil {
		return nil, SearchState{}, err
	}
	rs := make([]row, 0, len(ids))
	seen := map[int64]bool{}
	for _, id := range ids {
		if r, ok := byID[id]; ok && !seen[id] {
			seen[id] = true
			rs = append(rs, r)
		}
	}
	return rs, SearchState{Complete: done, Trackers: status}, nil
}

// ForgetSearches — настройки источников сменили (адрес, ключ): законченные поиски из кэша не берутся —
// иначе тот же запрос ещё 30 минут отвечал бы прежним набором (финальное ревью 11b-Д).
func (c *Catalog) ForgetSearches() {
	c.mu.Lock()
	clear(c.searches)
	c.mu.Unlock()
}

// searchSources — кто сейчас ищет: свои трекеры с адресом и источник поиска с адресом. Вызывать под c.mu.
func (c *Catalog) searchSources() []string {
	var names []string
	for name := range c.sources {
		if c.configured(name) { // без адреса трекер выключен: в поиске его нет
			names = append(names, name)
		}
	}
	if c.extra != nil && c.extra.Configured() {
		names = append(names, c.extra.Name())
	}
	slices.Sort(names)
	return names
}
