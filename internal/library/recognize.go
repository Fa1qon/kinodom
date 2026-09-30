package library

import (
	"context"
	"errors"

	"kinodom/internal/meta"
)

// KP — Кинопоиск для медиатеки (meta.Kinopoisk): тот же клиент и ограничитель, что у рейтингов.
type KP interface {
	Search(ctx context.Context, keyword string, year int) ([]meta.Film, error)
	Details(ctx context.Context, id int) (meta.FilmDetails, error)
}

// Состояния распознавания единицы (lib_units.state).
const (
	StateNew          = "new"          // ещё не искали
	StateFound        = "found"        // номер Кинопоиска найден
	StateUnrecognized = "unrecognized" // не нашли уверенно — «Не распознано»
	StateWait         = "wait"         // квота кончилась или Кинопоиск сбоит — искать позже
	StateManual       = "manual"       // размечено вручную (название и год, без Кинопоиска)
	StatePlain        = "plain"        // категория без Кинопоиска — название из имени
)

// Result — итог распознавания.
type Result struct {
	KP    int
	State string
}

// seriesTypes, filmTypes — типы Кинопоиска для устройства категории.
var (
	seriesTypes = map[string]bool{"TV_SERIES": true, "MINI_SERIES": true, "TV_SHOW": true}
	filmTypes   = map[string]bool{"FILM": true, "VIDEO": true}
)

// recognize — номер Кинопоиска единицы (спека, раздел 5.4): метка [kp…]; иначе поиск по названию и
// другим названиям (alt — у скачанного русское и оригинальное из названия раздачи), для латиницы — и
// по вариантам транслита; привязка — только при одном точном совпадении. checkType — у папок
// категорий: тип фильма должен подходить устройству. Ошибка — только отмена ctx.
func recognize(ctx context.Context, kp KP, p Parsed, alt []string, layout Layout, checkType bool) (Result, error) {
	if p.KP > 0 {
		return Result{KP: p.KP, State: StateFound}, nil
	}
	var queries []string
	targets := map[string]bool{}
	for _, name := range append([]string{p.Title}, alt...) {
		for _, v := range Variants(name) {
			if n := Norm(v); n != "" && !targets[n] {
				targets[n] = true
				queries = append(queries, v)
			}
		}
	}
	troubled := false
	for _, q := range queries {
		films, err := kp.Search(ctx, q, p.Year)
		if err != nil {
			if ctx.Err() != nil {
				return Result{}, ctx.Err()
			}
			if errors.Is(err, meta.ErrQuota) || errors.Is(err, meta.ErrRateLimited) || errors.Is(err, meta.ErrNoKey) || errors.Is(err, meta.ErrBadKey) {
				return Result{State: StateWait}, nil
			}
			troubled = true // 500 на кириллице, сеть — следующий вариант
			continue
		}
		switch f, n := pick(films, targets, p.Year, layout, checkType); {
		case n == 1:
			return Result{KP: f.ID, State: StateFound}, nil
		case n > 1: // несколько точных совпадений — другой вариант не разрешит, а ошибиться может
			return Result{State: StateUnrecognized}, nil
		}
	}
	if troubled {
		return Result{State: StateWait}, nil
	}
	return Result{State: StateUnrecognized}, nil
}

// pick — фильмы выдачи, у которых русское или оригинальное название совпадает с одним из искомых,
// год — ±1 (если известен), тип — подходит устройству (если checkType): первый из них и сколько их.
func pick(films []meta.Film, targets map[string]bool, year int, layout Layout, checkType bool) (meta.Film, int) {
	var found []meta.Film
	for _, f := range films {
		if !targets[Norm(f.NameRu)] && !targets[Norm(f.NameOrig)] {
			continue
		}
		if year > 0 && (f.Year == 0 || f.Year < year-1 || f.Year > year+1) {
			continue
		}
		if checkType {
			types := filmTypes
			if layout == LayoutSeries {
				types = seriesTypes
			}
			if !types[f.Type] {
				continue
			}
		}
		dup := false
		for _, x := range found {
			dup = dup || x.ID == f.ID
		}
		if !dup {
			found = append(found, f)
		}
	}
	if len(found) == 0 {
		return meta.Film{}, 0
	}
	return found[0], len(found)
}
