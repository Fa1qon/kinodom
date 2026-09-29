package catalog

import (
	"context"
	"errors"
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
	run, q, err := c.startSearch(query, false)
	if err != nil {
		return SearchState{}, err
	}
	return c.searchState(ctx, q, run)
}

// startSearch запускает поиск или берёт идущий (законченный без ошибок — не раньше 30 минут назад).
// poll — повторный опрос того же поиска: законченный с ошибкой трекера поиск он не запускает заново.
// Возвращает и запрос без лишних пробелов.
func (c *Catalog) startSearch(query string, poll bool) (*searchRun, string, error) {
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
	run, ok := c.searches[key]
	if ok && !poll && run.staleWithErrors(now) {
		ok = false // поиск с ошибками не кэшируется: новый запрос ищет заново, повторный опрос — нет
	}
	if !ok {
		run = &searchRun{started: now, status: map[string]string{}}
		for name := range c.sources {
			run.status[name] = SearchRunning
		}
		c.searches[key] = run
		parent := c.runCtx
		if parent == nil {
			parent = context.Background()
		}
		sctx, cancel := context.WithTimeout(parent, searchLimit)
		go c.runSearch(sctx, cancel, run, q)
	}
	c.mu.Unlock()
	return run, q, nil
}

// runSearch — все трекеры одновременно; каждый результат — в базу и в поиск сразу, как пришёл.
func (c *Catalog) runSearch(ctx context.Context, cancel context.CancelFunc, run *searchRun, q string) {
	defer cancel()
	var wg sync.WaitGroup
	for name, src := range c.sources {
		wg.Go(func() {
			rs, err := src.Search(ctx, q)
			var partial *source.PartialError
			switch {
			case errors.Is(err, context.DeadlineExceeded) && len(rs) == 0:
				err = errors.New("не ответил за 30 с")
			case errors.Is(err, context.Canceled) && len(rs) == 0:
				err = errors.New("каталог остановлен") // модуль остановили посреди поиска (ревью 5c)
			case err != nil && !errors.As(err, &partial):
				rs = nil
			}
			ids, serr := c.st.saveFound(context.WithoutCancel(ctx), rs, c.now())
			if serr != nil {
				c.log.Error("поиск: найденное не записалось", "err", serr)
				err = errors.Join(err, serr)
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
	rs = collapse(rs)
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
