package recs

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"
)

// «Похожие» Кинопоиска для фильмов профиля, у которых нет связей в пакете: сериалы (в MovieLens их
// почти нет), русское кино, новинки. Ключ Кинопоиска общий с рейтингами каталога (бесплатный — 500
// запросов в сутки), поэтому запросов немного: самые любимые первыми, не больше similarsPerDay в сутки.

// SimilarItem — фильм из «Похожих».
type SimilarItem struct {
	KP        int
	Title     string
	TitleOrig string
	Poster    string
}

// ErrSimilarsQuota — лимит ключа исчерпан: до завтра не спрашивать.
var ErrSimilarsQuota = errors.New("лимит ключа Кинопоиска")

// SimilarsSource — «Похожие» по ключу Кинопоиска приложения.
type SimilarsSource func(ctx context.Context, kp int) ([]SimilarItem, error)

const (
	similarsPerDay   = 40
	similarsRetry    = 30 * 24 * time.Hour
	similarsInterval = 3 * time.Second
)

// kpWeight — вес связи из «Похожих» по месту в списке, на шкале EASE пакета (медиана веса первого
// соседа 0.017, двадцатого 0.008) — как у сериалов, собранных в пакет.
func kpWeight(rank int) float64 { return 0.017 / (1 + 0.05*float64(rank)) }

type similarsFetcher struct {
	mu      sync.Mutex
	pending map[int]bool
	queue   chan int
	day     string
	spent   int
	paused  time.Time
}

// StartSimilars запускает дозапрос «Похожих»; src nil (нет ключа) — только связи пакета.
func (s *Service) StartSimilars(ctx context.Context, src SimilarsSource) {
	if src == nil {
		return
	}
	f := &similarsFetcher{pending: map[int]bool{}, queue: make(chan int, 100)}
	s.similars = f
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case kp := <-f.queue:
				s.fetchSimilars(ctx, src, f, kp)
				f.mu.Lock()
				delete(f.pending, kp)
				f.mu.Unlock()
				select {
				case <-time.After(similarsInterval):
				case <-ctx.Done():
					return
				}
			}
		}
	}()
}

func (s *Service) fetchSimilars(ctx context.Context, src SimilarsSource, f *similarsFetcher, kp int) {
	now := s.now()
	f.mu.Lock()
	if day := now.Format("2006-01-02"); f.day != day {
		f.day, f.spent = day, 0
	}
	if f.spent >= similarsPerDay || now.Before(f.paused) {
		f.mu.Unlock()
		return
	}
	f.spent++
	f.mu.Unlock()
	items, err := src(ctx, kp)
	if errors.Is(err, ErrSimilarsQuota) {
		f.mu.Lock()
		y, m, d := now.Date()
		f.paused = time.Date(y, m, d+1, 0, 0, 0, 0, now.Location())
		f.mu.Unlock()
		return
	}
	if err != nil && !errors.Is(err, ErrNoCard) {
		return // сеть или сбой — спросим при следующей подборке
	}
	at := now.UnixMilli()
	for r, it := range items {
		s.DB.W.ExecContext(ctx, `INSERT OR REPLACE INTO rec_similars (kp_id, other_kp, rank) VALUES (?, ?, ?)`, kp, it.KP, r)
		// карточка-заготовка: название и постер уже есть; вид и рейтинг дозапросит очередь карточек
		s.DB.W.ExecContext(ctx, `INSERT INTO rec_cards (kp_id, title, title_orig, poster, full, fetched_at) VALUES (?, ?, ?, ?, 0, ?)
			ON CONFLICT(kp_id) DO NOTHING`, it.KP, it.Title, it.TitleOrig, it.Poster, at)
	}
	s.DB.W.ExecContext(ctx, `INSERT OR REPLACE INTO rec_similars_state (kp_id, fetched_at) VALUES (?, ?)`, kp, at)
}

// localNeighbors — связи из дозапрошенных «Похожих» для фильмов ids: fn(откуда, куда, вес).
func (s *Service) localNeighbors(ctx context.Context, ids []int, fn func(from, to int, w float64)) error {
	for chunk := range slices.Chunk(ids, 500) {
		rows, err := s.DB.R.QueryContext(ctx, `SELECT kp_id, other_kp, rank FROM rec_similars WHERE kp_id IN (`+ph(len(chunk))+`)`, ints(chunk)...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var a, b, r int
			rows.Scan(&a, &b, &r)
			fn(a, b, kpWeight(r))
		}
		rows.Close()
	}
	return nil
}

// needSimilars ставит в очередь любимые фильмы без связей в пакете (самые любимые первыми), у которых
// «Похожие» ещё не спрашивали в последний месяц.
func (s *Service) needSimilars(ctx context.Context, prof Profile, linked map[int]bool) {
	f := s.similars
	if f == nil {
		return
	}
	var ids []int
	for kp, x := range prof {
		if x > 0 && !linked[kp] {
			ids = append(ids, kp)
		}
	}
	if len(ids) == 0 {
		return
	}
	asked := map[int]bool{}
	retry := s.now().Add(-similarsRetry).UnixMilli()
	for chunk := range slices.Chunk(ids, 500) {
		args := append(ints(chunk), retry)
		rows, err := s.DB.R.QueryContext(ctx, `SELECT kp_id FROM rec_similars_state WHERE kp_id IN (`+ph(len(chunk))+`) AND fetched_at > ?`, args...)
		if err != nil {
			return
		}
		for rows.Next() {
			var kp int
			rows.Scan(&kp)
			asked[kp] = true
		}
		rows.Close()
	}
	slices.SortFunc(ids, func(a, b int) int { return cmpDesc(prof[a], prof[b]) })
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, kp := range ids {
		if asked[kp] || f.pending[kp] {
			continue
		}
		select {
		case f.queue <- kp:
			f.pending[kp] = true
		default:
			return
		}
	}
}
