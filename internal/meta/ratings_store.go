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
	Prio     int
	Attempts int // неудач подряд
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
		 ON CONFLICT(release_id) DO UPDATE SET prio = excluded.prio,
		   -- Новые номера — новая информация: пробовать сразу. Иначе паузы после неудач остаются:
		   -- каталог ставит всё заново каждые 6 часов.
		   not_before = CASE WHEN kinopoisk_id != excluded.kinopoisk_id OR imdb_id != excluded.imdb_id THEN 0 ELSE not_before END,
		   attempts = CASE WHEN kinopoisk_id != excluded.kinopoisk_id OR imdb_id != excluded.imdb_id THEN 0 ELSE attempts END,
		   kinopoisk_id = excluded.kinopoisk_id, imdb_id = excluded.imdb_id, title = excluded.title`,
		it.Release, prio, it.KinopoiskID, it.IMDbID, it.Title)
	return err
}

// demoteBase — приоритет «после всего каталога»: каталог не бывает больше миллиона раздач.
const demoteBase = 1_000_000

// demoteAll отодвигает всю очередь за каталог; EnqueueCatalog затем возвращает места нынешнему.
func (s ratingStore) demoteAll(ctx context.Context) error {
	_, err := s.db.W.ExecContext(ctx, `UPDATE kp_queue SET prio = prio + ? WHERE prio < ?`, demoteBase, demoteBase)
	return err
}

// next — первая задача очереди, которую можно делать сейчас. keylessOnly — только те, где номер
// фильма уже известен: без ключа (или без квоты) искать нечем, а рейтинг по номеру — можно.
func (s ratingStore) next(ctx context.Context, now time.Time, keylessOnly bool) (queued, bool, error) {
	var q queued
	err := s.db.R.QueryRowContext(ctx,
		`SELECT q.release_id, q.prio, q.kinopoisk_id, q.imdb_id, q.title, q.attempts FROM kp_queue q
		 WHERE q.not_before <= ?
		   AND (? = 0 OR q.kinopoisk_id != 0 OR EXISTS (
		        SELECT 1 FROM kp_releases r WHERE r.release_id = q.release_id AND r.kp_id IS NOT NULL))
		 ORDER BY q.prio, q.release_id LIMIT 1`, ms(now), keylessOnly).
		Scan(&q.Release, &q.Prio, &q.KinopoiskID, &q.IMDbID, &q.Title, &q.Attempts)
	if errors.Is(err, sql.ErrNoRows) {
		return queued{}, false, nil
	}
	return q, err == nil, err
}

func (s ratingStore) postpone(ctx context.Context, release string, until time.Time) error {
	_, err := s.db.W.ExecContext(ctx, `UPDATE kp_queue SET not_before = ? WHERE release_id = ?`, ms(until), release)
	return err
}

// failed — ещё одна неудача подряд: не раньше until.
func (s ratingStore) failed(ctx context.Context, release string, until time.Time) error {
	_, err := s.db.W.ExecContext(ctx, `UPDATE kp_queue SET not_before = ?, attempts = attempts + 1 WHERE release_id = ?`, ms(until), release)
	return err
}

// done убирает сделанную задачу — только если её не обновили, пока она решалась: новые номера
// из описания, пришедшие в это время, не должны теряться.
func (s ratingStore) done(ctx context.Context, q queued) error {
	_, err := s.db.W.ExecContext(ctx,
		`DELETE FROM kp_queue WHERE release_id = ? AND kinopoisk_id = ? AND imdb_id = ? AND title = ?`,
		q.Release, q.KinopoiskID, q.IMDbID, q.Title)
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

// addFilm — фильм медиатеки: названия, год и тип обновляются; рейтинг — только ненулевой.
func (s ratingStore) addFilm(ctx context.Context, f Film, now time.Time) error {
	at := int64(0)
	if f.Rating > 0 {
		at = ms(now)
	}
	_, err := s.db.W.ExecContext(ctx,
		`INSERT INTO kp_films(kp_id, imdb_id, name_ru, name_orig, year, type, rating, rating_imdb, rating_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(kp_id) DO UPDATE SET
		   imdb_id = CASE WHEN excluded.imdb_id != '' THEN excluded.imdb_id ELSE imdb_id END,
		   name_ru = excluded.name_ru, name_orig = excluded.name_orig,
		   year = CASE WHEN excluded.year != 0 THEN excluded.year ELSE year END,
		   type = CASE WHEN excluded.type != '' THEN excluded.type ELSE type END,
		   rating = CASE WHEN excluded.rating > 0 THEN excluded.rating ELSE rating END,
		   rating_imdb = CASE WHEN excluded.rating > 0 THEN excluded.rating_imdb ELSE rating_imdb END,
		   rating_at = CASE WHEN excluded.rating > 0 THEN excluded.rating_at ELSE rating_at END`,
		f.ID, f.IMDbID, f.NameRu, f.NameOrig, f.Year, f.Type, f.Rating, f.RatingIMDb, at)
	return err
}

// films — рейтинги по номерам Кинопоиска.
func (s ratingStore) films(ctx context.Context, ids []int) (map[int]Rating, error) {
	out := map[int]Rating{}
	if len(ids) == 0 {
		return out, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := s.db.R.QueryContext(ctx, `SELECT kp_id, rating, rating_imdb, name_ru, name_orig, year FROM kp_films
		WHERE kp_id IN (?`+strings.Repeat(", ?", len(ids)-1)+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var r Rating
		if err := rows.Scan(&r.KinopoiskID, &r.Kinopoisk, &r.IMDb, &r.NameRu, &r.NameOrig, &r.Year); err != nil {
			return nil, err
		}
		out[r.KinopoiskID] = r
	}
	return out, rows.Err()
}

// setRating — только рейтинги (путь без ключа): остальное о фильме не трогаем.
func (s ratingStore) setRating(ctx context.Context, kpID int, kp, imdb float64, at time.Time) error {
	_, err := s.db.W.ExecContext(ctx,
		`INSERT INTO kp_films(kp_id, rating, rating_imdb, rating_at) VALUES(?, ?, ?, ?)
		 ON CONFLICT(kp_id) DO UPDATE SET rating = excluded.rating, rating_imdb = excluded.rating_imdb,
		   rating_at = excluded.rating_at`, kpID, kp, imdb, ms(at))
	return err
}

// imdbLink — что известно об IMDb: сначала кэш запросов, затем фильмы, у которых он указан.
func (s ratingStore) imdbLink(ctx context.Context, imdbID string) (kpID int, retryAt time.Time, found bool, err error) {
	var id sql.NullInt64
	var at int64
	err = s.db.R.QueryRowContext(ctx, `SELECT kp_id, retry_at FROM kp_imdb WHERE imdb_id = ?`, imdbID).Scan(&id, &at)
	if err == nil {
		return int(id.Int64), fromMS(at), true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, time.Time{}, false, err
	}
	err = s.db.R.QueryRowContext(ctx, `SELECT kp_id FROM kp_films WHERE imdb_id = ? LIMIT 1`, imdbID).Scan(&kpID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, time.Time{}, false, nil
	}
	return kpID, time.Time{}, err == nil, err
}

func (s ratingStore) setIMDb(ctx context.Context, imdbID string, kpID int, retryAt time.Time) error {
	_, err := s.db.W.ExecContext(ctx,
		`INSERT INTO kp_imdb(imdb_id, kp_id, retry_at) VALUES(?, ?, ?)
		 ON CONFLICT(imdb_id) DO UPDATE SET kp_id = excluded.kp_id, retry_at = excluded.retry_at`,
		imdbID, nullID(kpID), ms(retryAt))
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

// releasesOf — раздачи, для которых найден фильм, по номерам фильмов; у каждого фильма — по порядку.
func (s ratingStore) releasesOf(ctx context.Context, kpIDs []int) (map[int][]string, error) {
	out := map[int][]string{}
	for len(kpIDs) > 0 {
		chunk := kpIDs[:min(len(kpIDs), 500)]
		kpIDs = kpIDs[len(chunk):]
		args := make([]any, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}
		rows, err := s.db.R.QueryContext(ctx,
			`SELECT release_id, kp_id FROM kp_releases WHERE kp_id IN (?`+strings.Repeat(", ?", len(chunk)-1)+`) ORDER BY release_id`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var release string
			var id int
			if err := rows.Scan(&release, &id); err != nil {
				rows.Close()
				return nil, err
			}
			out[id] = append(out[id], release)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}
