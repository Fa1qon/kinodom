// Package recs — рекомендации «что посмотреть» прямо в Kinodom: вкус человека из журнала просмотров
// (history.view_log), оценок и отметок, похожесть фильмов из пакета модели (recs-model.db: EASE по
// MovieLens, настроение из Tag Genome и описаний), подбор на нескольких профилей семьи.
//
// Пакет модели собирает отдельный набор инструментов (kinosovet: обучение, связки с Кинопоиском,
// карточки) — здесь он только читается. Все номера — Кинопоиска.
package recs

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
)

// ErrNoModel — пакета модели нет: рекомендации выключены, остальной Kinodom работает.
var ErrNoModel = errors.New("пакет рекомендаций не установлен")

// Film — фильм пакета.
type Film struct {
	KP          int     `json:"id"`
	Kind        string  `json:"kind"` // film | series
	Title       string  `json:"title"`
	TitleOrig   string  `json:"titleOrig"`
	Year        int     `json:"year"`
	Genres      string  `json:"genres"`
	Duration    int     `json:"duration"`
	Description string  `json:"description"`
	Poster      string  `json:"-"` // адрес Кинопоиска; пульту — через /api/v1/recs/films/{id}/poster
	RatingKP    float64 `json:"kinopoisk"`
	RatingIMDb  float64 `json:"imdb"`
	Popularity  float64 `json:"-"`
}

// Model — пакет модели только для чтения.
type Model struct {
	db    *sql.DB
	Path  string
	Info  map[string]string
	mu    sync.Mutex
	niche float64
}

// OpenModel открывает пакет; файла нет — ErrNoModel.
func OpenModel(path string) (*Model, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, ErrNoModel
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=query_only(ON)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	m := &Model{db: db, Path: path, Info: map[string]string{}}
	rows, err := db.Query(`SELECT key, value FROM meta`)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("пакет рекомендаций повреждён: %w", err)
	}
	for rows.Next() {
		var k, v string
		rows.Scan(&k, &v)
		m.Info[k] = v
	}
	rows.Close()
	db.QueryRow(`SELECT popularity FROM films ORDER BY popularity DESC LIMIT 1 OFFSET 999`).Scan(&m.niche)
	return m, nil
}

func (m *Model) Close() error { return m.db.Close() }

func ph(n int) string { return strings.TrimSuffix(strings.Repeat("?,", n), ",") }

func ints(ids []int) []any {
	out := make([]any, len(ids))
	for i, v := range ids {
		out[i] = v
	}
	return out
}

const filmCols = `kp_id, kind, title_ru, title_orig, year, genres, duration, description, poster, rating_kp, rating_imdb, popularity`

func scanFilm(r interface{ Scan(...any) error }) (Film, error) {
	var f Film
	err := r.Scan(&f.KP, &f.Kind, &f.Title, &f.TitleOrig, &f.Year, &f.Genres, &f.Duration, &f.Description, &f.Poster, &f.RatingKP, &f.RatingIMDb, &f.Popularity)
	if f.Title == "" {
		f.Title = f.TitleOrig
	}
	return f, err
}

// Films — карточки по номерам (отсутствующие в пакете пропускаются).
func (m *Model) Films(ctx context.Context, ids []int) (map[int]Film, error) {
	out := map[int]Film{}
	for chunk := range slices.Chunk(ids, 500) {
		rows, err := m.db.QueryContext(ctx, `SELECT `+filmCols+` FROM films WHERE kp_id IN (`+ph(len(chunk))+`)`, ints(chunk)...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			f, err := scanFilm(rows)
			if err != nil {
				rows.Close()
				return nil, err
			}
			out[f.KP] = f
		}
		rows.Close()
	}
	return out, nil
}

// Film — одна карточка.
func (m *Model) Film(ctx context.Context, id int) (Film, bool) {
	f, err := scanFilm(m.db.QueryRowContext(ctx, `SELECT `+filmCols+` FROM films WHERE kp_id = ?`, id))
	return f, err == nil
}

// Neighbors — связи из фильмов ids: fn(откуда, куда, вес).
func (m *Model) Neighbors(ctx context.Context, ids []int, fn func(from, to int, w float64)) error {
	for chunk := range slices.Chunk(ids, 500) {
		rows, err := m.db.QueryContext(ctx, `SELECT kp_id, other_kp, weight FROM neighbors WHERE kp_id IN (`+ph(len(chunk))+`)`, ints(chunk)...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var a, b int
			var w float64
			rows.Scan(&a, &b, &w)
			fn(a, b, w)
		}
		rows.Close()
	}
	return nil
}

// MoodFilms — сильнее всего выражающие настроение (кандидаты для «хочу уютное» без истории).
func (m *Model) MoodFilms(ctx context.Context, moods []string, limit int) ([]int, error) {
	args := make([]any, 0, len(moods)+1)
	for _, x := range moods {
		args = append(args, x)
	}
	args = append(args, limit)
	rows, err := m.db.QueryContext(ctx, `SELECT t.kp_id FROM tags t JOIN films f ON f.kp_id = t.kp_id
		WHERE t.tag IN (`+ph(len(moods))+`) AND t.score >= 0.6
		GROUP BY t.kp_id ORDER BY MAX(t.score) * (1 + LOG(1 + MAX(f.popularity, 0))) DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int
	for rows.Next() {
		var id int
		rows.Scan(&id)
		out = append(out, id)
	}
	return out, rows.Err()
}

// Moods — сила запрошенных осей у фильмов: фильм → ось → 0..1.
func (m *Model) Moods(ctx context.Context, ids []int, moods []string) (map[int]map[string]float64, error) {
	out := map[int]map[string]float64{}
	for chunk := range slices.Chunk(ids, 500) {
		args := ints(chunk)
		for _, x := range moods {
			args = append(args, x)
		}
		rows, err := m.db.QueryContext(ctx, `SELECT kp_id, tag, score FROM tags WHERE kp_id IN (`+ph(len(chunk))+`) AND tag IN (`+ph(len(moods))+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id int
			var tag string
			var v float64
			rows.Scan(&id, &tag, &v)
			if out[id] == nil {
				out[id] = map[string]float64{}
			}
			out[id][tag] = v
		}
		rows.Close()
	}
	return out, nil
}

// Popular — любимое многими и высоко оценённое: старт без истории и онбординг.
func (m *Model) Popular(ctx context.Context, limit int) ([]int, error) {
	rows, err := m.db.QueryContext(ctx, `SELECT kp_id FROM films ORDER BY LOG(1 + popularity) * MAX(rating_kp, rating_imdb, 6) DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int
	for rows.Next() {
		var id int
		rows.Scan(&id)
		out = append(out, id)
	}
	return out, rows.Err()
}

// Search — фильмы пакета по названию («что-то как „Тьма“»): точное совпадение, начало, вхождение.
func (m *Model) Search(ctx context.Context, q string, limit int) ([]Film, error) {
	q = strings.TrimSpace(q)
	if q == "" {
		return nil, nil
	}
	rows, err := m.db.QueryContext(ctx, `SELECT `+filmCols+` FROM films WHERE title_ru LIKE ?1 OR title_orig LIKE ?1 OR title_ru LIKE ?2 OR title_orig LIKE ?2
		ORDER BY (lower(title_ru) = lower(?3) OR lower(title_orig) = lower(?3)) DESC, popularity DESC LIMIT ?4`,
		"%"+q+"%", "%"+upperFirst(q)+"%", q, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Film
	for rows.Next() {
		f, err := scanFilm(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func upperFirst(s string) string {
	r := []rune(s)
	if len(r) > 0 {
		r[0] = []rune(strings.ToUpper(string(r[0])))[0]
	}
	return string(r)
}
