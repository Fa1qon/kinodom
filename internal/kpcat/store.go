package kpcat

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"

	"kinodom/internal/meta"
	"kinodom/internal/store"
)

// db — таблицы модуля kpcat (миграция 0018_kpcat.sql).
type db struct{ *store.DB }

func ms(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

// film — фильм каталога из базы.
type film struct {
	ID                        int
	Type, NameRu, NameOrig    string
	Year                      int
	Rating                    float64
	Votes                     int
	Premiere                  int64
	Poster, Genres, Countries string
	IMDb                      float64
}

func (f film) view() FilmView {
	v := FilmView{ID: f.ID, Type: f.Type, Title: f.NameRu, Original: f.NameOrig, Year: f.Year, Kinopoisk: f.Rating, IMDb: f.IMDb,
		Votes: f.Votes, Genres: split(f.Genres), Countries: split(f.Countries)}
	if v.Title == "" {
		v.Title = f.NameOrig
	}
	if f.Premiere > 0 {
		t := time.UnixMilli(f.Premiere).UTC()
		v.Premiere = &t
	}
	if f.Poster != "" {
		v.Poster = "/api/v1/kpcat/films/" + strconv.Itoa(f.ID) + "/poster"
	}
	return v
}

func split(s string) []string {
	if s == "" {
		return []string{}
	}
	return strings.Split(s, ", ")
}

const filmColumns = `f.kp_id, f.type, f.name_ru, f.name_orig, f.year, f.rating, f.votes, f.premiere, f.poster, f.genres, f.countries, f.imdb`

func scanFilm(sc interface{ Scan(...any) error }) (film, error) {
	var f film
	err := sc.Scan(&f.ID, &f.Type, &f.NameRu, &f.NameOrig, &f.Year, &f.Rating, &f.Votes, &f.Premiere, &f.Poster, &f.Genres, &f.Countries, &f.IMDb)
	return f, err
}

// replaceSection — фильмы раздела: данные обновляются (IMDb остаётся), места заменяются целиком.
func (d db) replaceSection(ctx context.Context, sec string, fs []meta.ListFilm, now time.Time) error {
	tx, err := d.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM kpcat_entries WHERE section = ?`, sec); err != nil {
		return err
	}
	seen := map[int]bool{}
	pos := 0
	for _, f := range fs {
		if seen[f.ID] {
			continue // сайт мог повторить фильм на соседних страницах
		}
		seen[f.ID] = true
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO kpcat_films(kp_id, type, name_ru, name_orig, year, rating, votes, premiere, poster, genres, countries, updated_at)
			 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT(kp_id) DO UPDATE SET type = excluded.type, name_ru = excluded.name_ru, name_orig = excluded.name_orig,
			   year = excluded.year, rating = excluded.rating, votes = excluded.votes, premiere = excluded.premiere,
			   poster = excluded.poster, genres = excluded.genres, countries = excluded.countries, updated_at = excluded.updated_at`,
			f.ID, f.Type, f.NameRu, f.NameOrig, f.Year, f.Rating, f.Votes, ms(f.Premiere), f.Poster,
			strings.Join(f.Genres, ", "), strings.Join(f.Countries, ", "), ms(now)); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO kpcat_entries(section, position, kp_id) VALUES(?, ?, ?)`, sec, pos, f.ID); err != nil {
			return err
		}
		pos++
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO kpcat_state(section, refreshed_at, total) VALUES(?, ?, ?)
		 ON CONFLICT(section) DO UPDATE SET refreshed_at = excluded.refreshed_at, total = excluded.total`, sec, ms(now), pos); err != nil {
		return err
	}
	return tx.Commit()
}

// freshSections — разделы, обновлённые не раньше since.
func (d db) freshSections(ctx context.Context, since time.Time) (map[string]bool, error) {
	rows, err := d.R.QueryContext(ctx, `SELECT section FROM kpcat_state WHERE refreshed_at >= ?`, ms(since))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out[s] = true
	}
	return out, rows.Err()
}

// newKey — дата выхода для «Новых», мс: премьера, без неё — 1 января года (ревью 14Г: иначе фильм с годом, но без
// даты — в самом хвосте), без года — 0.
const newKey = `(CASE WHEN f.premiere > 0 THEN f.premiere WHEN f.year > 0 THEN CAST(strftime('%s', printf('%04d-01-01', f.year)) AS INTEGER) * 1000 ELSE 0 END)`

// orderBy — ORDER BY порядка: IMDb без оценки и новизна без даты — в конце (Review Focus 2, 3); равные — по месту.
var orderBy = map[string]string{
	"popular": `e.position`,
	"kp":      `f.rating DESC, e.position`,
	"imdb":    `(f.imdb = 0), f.imdb DESC, e.position`,
	// Новизна: ещё не вышедшие (дата впереди) — в конце, как без даты (ревью 14Г); ? — сейчас.
	"new": `(` + newKey + ` = 0 OR ` + newKey + ` > ?), ` + newKey + ` DESC, e.position`,
}

func (d db) list(ctx context.Context, sec, order string, offset, limit int, now time.Time) ([]film, int, error) {
	var total int
	if err := d.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM kpcat_entries WHERE section = ?`, sec).Scan(&total); err != nil {
		return nil, 0, err
	}
	args := []any{sec}
	if order == "new" {
		args = append(args, ms(now))
	}
	rows, err := d.R.QueryContext(ctx,
		`SELECT `+filmColumns+` FROM kpcat_entries e JOIN kpcat_films f ON f.kp_id = e.kp_id
		 WHERE e.section = ? ORDER BY `+orderBy[order]+` LIMIT ? OFFSET ?`, append(args, limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []film
	for rows.Next() {
		f, err := scanFilm(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, f)
	}
	return out, total, rows.Err()
}

// orderIDs — номера фильмов раздела в порядке order: снимок для порций (ревью 14Г).
func (d db) orderIDs(ctx context.Context, sec, order string, now time.Time) ([]int, error) {
	args := []any{sec}
	if order == "new" {
		args = append(args, ms(now))
	}
	rows, err := d.R.QueryContext(ctx,
		`SELECT e.kp_id FROM kpcat_entries e JOIN kpcat_films f ON f.kp_id = e.kp_id WHERE e.section = ? ORDER BY `+orderBy[order], args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// filmsByIDs — фильмы по номерам в их порядке (фильмы из базы не удаляются — нет только неизвестных).
func (d db) filmsByIDs(ctx context.Context, ids []int) ([]film, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := d.R.QueryContext(ctx,
		`SELECT `+filmColumns+` FROM kpcat_films f WHERE f.kp_id IN (`+strings.Repeat("?, ", len(ids)-1)+`?)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	by := make(map[int]film, len(ids))
	for rows.Next() {
		f, err := scanFilm(rows)
		if err != nil {
			return nil, err
		}
		by[f.ID] = f
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]film, 0, len(ids))
	for _, id := range ids {
		if f, ok := by[id]; ok {
			out = append(out, f)
		}
	}
	return out, nil
}

var errNoFilm = errors.New("такого фильма в каталоге Кинопоиска нет")

func (d db) film(ctx context.Context, id int) (film, error) {
	f, err := scanFilm(d.R.QueryRowContext(ctx, `SELECT `+filmColumns+` FROM kpcat_films f WHERE f.kp_id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return film{}, errNoFilm
	}
	return f, err
}

// sectionState — фильмов в разделе и когда он обновлён (нулевое — ни разу).
type sectionState struct {
	count int
	at    time.Time
}

// counts — состояние разделов и последнее обновление из всех.
func (d db) counts(ctx context.Context) (map[string]sectionState, time.Time, error) {
	rows, err := d.R.QueryContext(ctx, `SELECT section, total, refreshed_at FROM kpcat_state`)
	if err != nil {
		return nil, time.Time{}, err
	}
	defer rows.Close()
	out := map[string]sectionState{}
	var last time.Time
	for rows.Next() {
		var s string
		var n int
		var at int64
		if err := rows.Scan(&s, &n, &at); err != nil {
			return nil, time.Time{}, err
		}
		st := sectionState{count: n}
		if at > 0 {
			st.at = time.UnixMilli(at)
		}
		out[s] = st
		if st.at.After(last) {
			last = st.at
		}
	}
	return out, last, rows.Err()
}

// imdbDue — фильмы разделов, у которых IMDb не спрашивали или спрашивали раньше before: ближе к началу — первыми.
func (d db) imdbDue(ctx context.Context, before time.Time, n int) ([]int, error) {
	rows, err := d.R.QueryContext(ctx,
		`SELECT f.kp_id FROM kpcat_films f JOIN kpcat_entries e ON e.kp_id = f.kp_id
		 WHERE f.imdb_at < ? GROUP BY f.kp_id ORDER BY MIN(e.position), f.kp_id LIMIT ?`, ms(before), n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// saveIMDbRetry — оценка не пришла из-за сбоя: спросить снова, когда at устареет.
func (d db) saveIMDbRetry(ctx context.Context, id int, at time.Time) error {
	_, err := d.W.ExecContext(ctx, `UPDATE kpcat_films SET imdb_at = ? WHERE kp_id = ?`, ms(at), id)
	return err
}

// posters — адреса постеров фильмов разделов.
func (d db) posters(ctx context.Context) ([]string, error) {
	rows, err := d.R.QueryContext(ctx,
		`SELECT DISTINCT f.poster FROM kpcat_films f JOIN kpcat_entries e ON e.kp_id = f.kp_id WHERE f.poster != ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (d db) saveIMDb(ctx context.Context, id int, r float64, votes int, now time.Time) error {
	_, err := d.W.ExecContext(ctx, `UPDATE kpcat_films SET imdb = ?, imdb_votes = ?, imdb_at = ? WHERE kp_id = ?`, r, votes, ms(now), id)
	return err
}
