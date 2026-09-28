package meta

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"kinodom/internal/store"
)

// ratingStore — таблицы модуля ratings (миграция 0003_ratings.sql). Другие модули в них не
// ходят: только через Ratings (спека, раздел 3).
type ratingStore struct{ db *store.DB }

// queued — строка очереди.
type queued struct {
	Item
	Prio int
}

func ms(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func fromMS(v int64) time.Time {
	if v == 0 {
		return time.Time{}
	}
	return time.UnixMilli(v)
}

// nullID — 0 в базе как NULL: «не найдено».
func nullID(id int) any {
	if id == 0 {
		return nil
	}
	return id
}

func (s ratingStore) enqueue(ctx context.Context, prio int, it Item) error {
	_, err := s.db.W.ExecContext(ctx,
		`INSERT INTO kp_queue(release_id, prio, kinopoisk_id, imdb_id, title) VALUES(?, ?, ?, ?, ?)
		 ON CONFLICT(release_id) DO UPDATE SET prio = excluded.prio, kinopoisk_id = excluded.kinopoisk_id,
		   imdb_id = excluded.imdb_id, title = excluded.title`,
		it.Release, prio, it.KinopoiskID, it.IMDbID, it.Title)
	return err
}

// next — первая задача очереди, которую можно делать сейчас. keylessOnly — только те, где номер
// фильма уже известен: без ключа (или без квоты) искать нечем, а рейтинг по номеру — можно.
func (s ratingStore) next(ctx context.Context, now time.Time, keylessOnly bool) (queued, bool, error) {
	var q queued
	err := s.db.R.QueryRowContext(ctx,
		`SELECT q.release_id, q.prio, q.kinopoisk_id, q.imdb_id, q.title FROM kp_queue q
		 WHERE q.not_before <= ?
		   AND (? = 0 OR q.kinopoisk_id != 0 OR EXISTS (
		        SELECT 1 FROM kp_releases r WHERE r.release_id = q.release_id AND r.kp_id IS NOT NULL))
		 ORDER BY q.prio, q.release_id LIMIT 1`, ms(now), keylessOnly).
		Scan(&q.Release, &q.Prio, &q.KinopoiskID, &q.IMDbID, &q.Title)
	if errors.Is(err, sql.ErrNoRows) {
		return queued{}, false, nil
	}
	return q, err == nil, err
}

func (s ratingStore) postpone(ctx context.Context, release string, until time.Time) error {
	_, err := s.db.W.ExecContext(ctx, `UPDATE kp_queue SET not_before = ? WHERE release_id = ?`, ms(until), release)
	return err
}

func (s ratingStore) done(ctx context.Context, release string) error {
	_, err := s.db.W.ExecContext(ctx, `DELETE FROM kp_queue WHERE release_id = ?`, release)
	return err
}

func (s ratingStore) queueLen(ctx context.Context) (int, error) {
	var n int
	err := s.db.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM kp_queue`).Scan(&n)
	return n, err
}

// link — раздача → фильм; kpID = 0 — не найдено до retryAt.
func (s ratingStore) link(ctx context.Context, release string, kpID int, retryAt time.Time) error {
	_, err := s.db.W.ExecContext(ctx,
		`INSERT INTO kp_releases(release_id, kp_id, retry_at) VALUES(?, ?, ?)
		 ON CONFLICT(release_id) DO UPDATE SET kp_id = excluded.kp_id, retry_at = excluded.retry_at`,
		release, nullID(kpID), ms(retryAt))
	return err
}

// releaseLink — что известно о раздаче: found = false — ещё не искали.
func (s ratingStore) releaseLink(ctx context.Context, release string) (kpID int, retryAt time.Time, found bool, err error) {
	var id sql.NullInt64
	var at int64
	err = s.db.R.QueryRowContext(ctx, `SELECT kp_id, retry_at FROM kp_releases WHERE release_id = ?`, release).Scan(&id, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, time.Time{}, false, nil
	}
	return int(id.Int64), fromMS(at), err == nil, err
}

func (s ratingStore) setTitle(ctx context.Context, title string, year, kpID int, retryAt time.Time) error {
	_, err := s.db.W.ExecContext(ctx,
		`INSERT INTO kp_titles(title, year, kp_id, retry_at) VALUES(?, ?, ?, ?)
		 ON CONFLICT(title, year) DO UPDATE SET kp_id = excluded.kp_id, retry_at = excluded.retry_at`,
		title, year, nullID(kpID), ms(retryAt))
	return err
}

func (s ratingStore) titleLink(ctx context.Context, title string, year int) (kpID int, retryAt time.Time, found bool, err error) {
	var id sql.NullInt64
	var at int64
	err = s.db.R.QueryRowContext(ctx, `SELECT kp_id, retry_at FROM kp_titles WHERE title = ? AND year = ?`, title, year).Scan(&id, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, time.Time{}, false, nil
	}
	return int(id.Int64), fromMS(at), err == nil, err
}

func (s ratingStore) saveFilm(ctx context.Context, f Film, ratingAt time.Time) error {
	_, err := s.db.W.ExecContext(ctx,
		`INSERT INTO kp_films(kp_id, imdb_id, name_ru, name_orig, year, type, rating, rating_imdb, rating_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(kp_id) DO UPDATE SET imdb_id = excluded.imdb_id, name_ru = excluded.name_ru,
		   name_orig = excluded.name_orig, year = excluded.year, type = excluded.type,
		   rating = excluded.rating, rating_imdb = excluded.rating_imdb, rating_at = excluded.rating_at`,
		f.ID, f.IMDbID, f.NameRu, f.NameOrig, f.Year, f.Type, f.Rating, f.RatingIMDb, ms(ratingAt))
	return err
}

// setRating — только рейтинги (путь без ключа): остальное о фильме не трогаем.
func (s ratingStore) setRating(ctx context.Context, kpID int, kp, imdb float64, at time.Time) error {
	_, err := s.db.W.ExecContext(ctx,
		`INSERT INTO kp_films(kp_id, rating, rating_imdb, rating_at) VALUES(?, ?, ?, ?)
		 ON CONFLICT(kp_id) DO UPDATE SET rating = excluded.rating, rating_imdb = excluded.rating_imdb,
		   rating_at = excluded.rating_at`, kpID, kp, imdb, ms(at))
	return err
}

// ratingAt — когда обновлён рейтинг фильма; found = false — фильма в базе нет.
func (s ratingStore) ratingAt(ctx context.Context, kpID int) (time.Time, bool, error) {
	var at int64
	err := s.db.R.QueryRowContext(ctx, `SELECT rating_at FROM kp_films WHERE kp_id = ?`, kpID).Scan(&at)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, false, nil
	}
	return fromMS(at), err == nil, err
}

// ratings — рейтинги раздач, у которых найден фильм.
func (s ratingStore) ratings(ctx context.Context, releases []string) (map[string]Rating, error) {
	out := map[string]Rating{}
	for len(releases) > 0 {
		chunk := releases[:min(len(releases), 500)]
		releases = releases[len(chunk):]
		args := make([]any, len(chunk))
		for i, r := range chunk {
			args[i] = r
		}
		rows, err := s.db.R.QueryContext(ctx,
			`SELECT r.release_id, f.kp_id, f.rating, f.rating_imdb, f.name_ru, f.name_orig, f.year
			 FROM kp_releases r JOIN kp_films f ON f.kp_id = r.kp_id
			 WHERE r.release_id IN (?`+strings.Repeat(", ?", len(chunk)-1)+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id string
			var r Rating
			if err := rows.Scan(&id, &r.KinopoiskID, &r.Kinopoisk, &r.IMDb, &r.NameRu, &r.NameOrig, &r.Year); err != nil {
				rows.Close()
				return nil, err
			}
			out[id] = r
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}
