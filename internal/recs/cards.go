package recs

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"
)

// Карточки фильмов. Пакет модели несёт полные карточки (русское название, постер, рейтинг) только у
// популярных фильмов — остальные Kinodom дозапрашивает с Кинопоиска сам, когда фильм попал в подборку,
// и хранит в rec_cards. Подборка показывается сразу; постер появляется при следующем обновлении экрана.

// Card — карточка с Кинопоиска.
type Card struct {
	Kind        string // film | series; "" — не знаем
	Title       string
	TitleOrig   string
	Year        int
	Genres      string
	Description string
	Poster      string
	RatingKP    float64
	RatingIMDb  float64
}

// ErrNoCard — у Кинопоиска такого фильма нет: не спрашивать неделю.
var ErrNoCard = errors.New("карточки нет")

// CardSource — откуда брать карточку (клиент Кинопоиска приложения); series — подсказка из пакета.
type CardSource func(ctx context.Context, kp int, series bool) (Card, error)

const (
	cardQueue    = 200 // больше ждущих — новые отбрасываются (дозапросятся при следующем показе)
	cardRetry    = 7 * 24 * time.Hour
	cardInterval = 2 * time.Second // свой темп поверх очереди клиента: подборка не съедает предел каталога
)

type cardFetcher struct {
	mu      sync.Mutex
	pending map[int]bool
	queue   chan cardJob
}

type cardJob struct {
	kp     int
	series bool
}

// StartCards запускает дозапрос карточек; без src (нет Кинопоиска) — карточки только из пакета.
func (s *Service) StartCards(ctx context.Context, src CardSource) {
	if src == nil {
		return
	}
	f := &cardFetcher{pending: map[int]bool{}, queue: make(chan cardJob, cardQueue)}
	s.cards = f
	go func() {
		tick := time.NewTicker(cardInterval)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case j := <-f.queue:
				s.fetchCard(ctx, src, j)
				f.mu.Lock()
				delete(f.pending, j.kp)
				f.mu.Unlock()
				select {
				case <-tick.C:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
}

func (s *Service) fetchCard(ctx context.Context, src CardSource, j cardJob) {
	c, err := src(ctx, j.kp, j.series)
	now := s.now().UnixMilli()
	switch {
	case errors.Is(err, ErrNoCard):
		s.DB.W.ExecContext(ctx, `INSERT INTO rec_cards (kp_id, missing, fetched_at) VALUES (?, 1, ?)
			ON CONFLICT(kp_id) DO UPDATE SET missing = 1, fetched_at = excluded.fetched_at`, j.kp, now)
	case err != nil:
		// Кинопоиск недоступен или пауза — попробуем при следующем показе
	default:
		s.DB.W.ExecContext(ctx, `INSERT INTO rec_cards (kp_id, kind, title, title_orig, year, genres, description, poster, rating_kp, rating_imdb, missing, full, fetched_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, 1, ?)
			ON CONFLICT(kp_id) DO UPDATE SET kind = excluded.kind, title = excluded.title, title_orig = excluded.title_orig, year = excluded.year,
				genres = excluded.genres, description = excluded.description, poster = excluded.poster, rating_kp = excluded.rating_kp,
				rating_imdb = excluded.rating_imdb, missing = 0, full = 1, fetched_at = excluded.fetched_at`,
			j.kp, c.Kind, c.Title, c.TitleOrig, c.Year, c.Genres, c.Description, c.Poster, c.RatingKP, c.RatingIMDb, now)
	}
}

// needCards ставит в очередь фильмы без карточки (без постера в пакете и без rec_cards).
func (s *Service) needCards(fs []Film) {
	f := s.cards
	if f == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, x := range fs {
		if (x.Poster != "" && !x.partial) || x.carded || f.pending[x.KP] {
			continue
		}
		select {
		case f.queue <- cardJob{kp: x.KP, series: x.Kind == "series"}:
			f.pending[x.KP] = true
		default:
			return
		}
	}
}

// withCards — фильмы пакета с карточками, дозапрошенными у Кинопоиска: их название, постер и рейтинг главнее.
// want — все запрошенные номера: фильмы вне пакета (из «Похожих») берутся только из rec_cards.
func (s *Service) withCards(ctx context.Context, fs map[int]Film, want []int) error {
	ids := make([]int, 0, len(want))
	for _, kp := range want {
		if f, ok := fs[kp]; !ok || f.Poster == "" {
			ids = append(ids, kp)
		}
	}
	retry := s.now().Add(-cardRetry).UnixMilli()
	for chunk := range slices.Chunk(ids, 500) {
		rows, err := s.DB.R.QueryContext(ctx, `SELECT kp_id, kind, title, title_orig, year, genres, description, poster, rating_kp, rating_imdb, missing, full, fetched_at
			FROM rec_cards WHERE kp_id IN (`+ph(len(chunk))+`)`, ints(chunk)...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var kp int
			var c Card
			var missing, full bool
			var at int64
			if err := rows.Scan(&kp, &c.Kind, &c.Title, &c.TitleOrig, &c.Year, &c.Genres, &c.Description, &c.Poster, &c.RatingKP, &c.RatingIMDb, &missing, &full, &at); err != nil {
				rows.Close()
				return err
			}
			f, inPack := fs[kp]
			if !inPack {
				if missing || c.Title == "" {
					continue
				}
				f = Film{KP: kp, Kind: "film"} // вне пакета: фильм из «Похожих» — только то, что знаем по карточке
			}
			if missing {
				f.carded = at > retry // неделю не спрашиваем снова
				fs[kp] = f
				continue
			}
			f.carded, f.partial = full, !full
			if c.Kind != "" {
				f.Kind = c.Kind
			}
			if c.Title != "" {
				f.Title = c.Title
			}
			if c.TitleOrig != "" {
				f.TitleOrig = c.TitleOrig
			}
			if c.Year > 0 {
				f.Year = c.Year
			}
			if c.Genres != "" {
				f.Genres = c.Genres
			}
			if c.Description != "" {
				f.Description = c.Description
			}
			f.Poster, f.RatingKP, f.RatingIMDb = c.Poster, c.RatingKP, max(c.RatingIMDb, f.RatingIMDb)
			fs[kp] = f
		}
		rows.Close()
	}
	return nil
}

// films — карточки пакета с дозапрошенными (вместо Model.Films везде, где фильмы показываются).
func (s *Service) films(ctx context.Context, m *Model, ids []int) (map[int]Film, error) {
	fs, err := m.Films(ctx, ids)
	if err != nil {
		return nil, err
	}
	return fs, s.withCards(ctx, fs, ids)
}

// film — одна карточка с дозапрошенной.
func (s *Service) film(ctx context.Context, m *Model, id int) (Film, bool) {
	fs, err := s.films(ctx, m, []int{id})
	f, ok := fs[id]
	return f, err == nil && ok
}
