package recs

import (
	"context"
	"math"
	"time"

	"kinodom/internal/history"
)

// Вклады в профиль вкуса (kinosovet/research/algorithms.md; ревью журнала 2026-10-29). Явная оценка
// всегда главнее просмотра.
const (
	xLove      = 1.3
	xLike      = 1.0
	xOK        = 0.4
	xDislike   = -0.5
	xHate      = -1.0
	xFinished  = 0.8 // досмотрел подряд
	xMostly    = 0.5 // 70–90 %
	xRewatch   = 1.0
	xRewatch3  = 1.3
	xElsewhere = 0.2 // «Просмотрено» отметкой — смотрел где-то ещё, без признаков, понравилось ли

	xAbandoned   = -0.7 // начал (≥ 2 % подряд) и бросил в первых 15 %
	xHalfDropped = -0.3 // бросил между 15 % и половиной
	xDeleted     = -0.9 // включил, неинтересно, удалил сам (замечание № 19 Kinodom — ровно этот случай)
	xDeletedCold = -0.4 // удалил сам, почти не начав, — передумал
	xPilotDrop   = -0.3 // сериал: 1–2 серии и давно не возвращался
	xSeries      = 0.8  // сериал: досмотрел 3+ серии
	xSeriesLong  = 1.0  // 6+ серий
	xNotMine     = -0.5
	xWant        = 0.3

	sampleSeen     = 0.02           // меньше — пробовал озвучку или качество, не «смотрел»
	abandonAfter   = 72 * time.Hour // не возвращался столько — фильм брошен, а не отложен
	pilotDropAfter = 14 * 24 * time.Hour
)

// watchState — журнал человека по одному фильму (все файлы раздач и медиатеки вместе).
type watchState struct {
	files, finished, times int
	seen                   float64 // лучшая доля подряд среди файлов
	manual                 bool    // досмотрен только отметкой
	deleted                bool    // удалил сам, не досмотрев
	lastAt                 time.Time
}

// Signals — всё, что известно о вкусе человека.
type Signals struct {
	Ratings map[int]int
	Marks   map[int]string // notmine | seen | want
	Watches map[int]*watchState
	Shown   map[int]time.Time
}

func newSignals() Signals {
	return Signals{Ratings: map[int]int{}, Marks: map[int]string{}, Watches: map[int]*watchState{}, Shown: map[int]time.Time{}}
}

// addViews — журнал просмотров (history.Views) по фильмам.
func (sg Signals) addViews(views []history.View) {
	for _, v := range views {
		if v.KP == 0 {
			continue
		}
		w := sg.Watches[v.KP]
		if w == nil {
			w = &watchState{manual: true}
			sg.Watches[v.KP] = w
		}
		w.files++
		w.seen = max(w.seen, v.Seen)
		w.times = max(w.times, v.Times)
		if v.Finished {
			w.finished++
		}
		if !(v.Finished && v.Seen < 0.3) {
			w.manual = false
		}
		if v.Ended == history.EndedDeleted && !v.Finished {
			w.deleted = true
		}
		if v.LastAt.After(w.lastAt) {
			w.lastAt = v.LastAt
		}
	}
}

// Seen — фильм человек уже знает: оценил, отметил, досмотрел, бросил или удалил. Только что открытое
// на пробу (озвучку посмотреть) — не считается.
func (sg Signals) Seen(kp int) bool {
	if _, ok := sg.Ratings[kp]; ok {
		return true
	}
	if m := sg.Marks[kp]; m == "seen" || m == "notmine" {
		return true
	}
	w := sg.Watches[kp]
	return w != nil && (w.finished > 0 || w.deleted || w.seen >= sampleSeen)
}

// Profile — вход модели: фильм → вклад (положительный — нравится).
type Profile map[int]float64

// Build — профиль вкуса из сигналов; now — чтобы отличить брошенное от отложенного.
func (sg Signals) Build(now time.Time) Profile {
	p := Profile{}
	mean, sd := ratingStats(sg.Ratings)
	for kp, v := range sg.Ratings {
		p[kp] = ratingWeight(v, mean, sd)
	}
	for kp, w := range sg.Watches {
		if _, ok := p[kp]; ok {
			continue
		}
		if x, ok := watchWeight(w, now); ok {
			p[kp] = x
		}
	}
	for kp, m := range sg.Marks {
		if _, ok := p[kp]; ok {
			continue
		}
		switch m {
		case "notmine":
			p[kp] = xNotMine
		case "want":
			p[kp] = xWant
		}
	}
	return p
}

func watchWeight(w *watchState, now time.Time) (float64, bool) {
	idle := now.Sub(w.lastAt)
	if w.files >= 2 { // сериал: по числу досмотренных серий
		switch {
		case w.finished >= 6:
			return xSeriesLong, true
		case w.finished >= 3:
			return xSeries, true
		case w.deleted:
			return xDeleted, true
		case w.finished >= 1 && idle > pilotDropAfter:
			return xPilotDrop, true
		case w.finished >= 1:
			return 0, false // смотрит
		}
	}
	switch {
	case w.times >= 3:
		return xRewatch3, true
	case w.times == 2:
		return xRewatch, true
	case w.finished > 0 && w.manual:
		return xElsewhere, true
	case w.finished > 0:
		return xFinished, true
	case w.deleted && w.seen >= sampleSeen:
		return xDeleted, true
	case w.deleted:
		return xDeletedCold, true
	case w.seen >= 0.7:
		return xMostly, true
	case idle < abandonAfter || w.seen < sampleSeen:
		return 0, false // смотрит сейчас или только пробовал
	case w.seen < 0.15:
		return xAbandoned, true
	case w.seen < 0.5:
		return xHalfDropped, true
	}
	return 0, false
}

// ratingStats — средняя и разброс оценок с поправкой на малое число (5 воображаемых оценок 7 ± 1.8).
func ratingStats(r map[int]int) (mean, sd float64) {
	const priorN, priorMean, priorSD = 5.0, 7.0, 1.8
	n := float64(len(r))
	var sum, sq float64
	for _, v := range r {
		sum += float64(v)
	}
	mean = (sum + priorN*priorMean) / (n + priorN)
	for _, v := range r {
		sq += (float64(v) - mean) * (float64(v) - mean)
	}
	sd = math.Sqrt((sq + priorN*priorSD*priorSD) / (n + priorN))
	return mean, max(sd, 1)
}

func ratingWeight(v int, mean, sd float64) float64 {
	z := (float64(v) - mean) / sd
	switch {
	case v <= 3 || z <= -1.2:
		return xHate
	case z <= -0.5:
		return xDislike
	case v >= 9 && z > 0.3:
		return xLove
	case z >= 0.5:
		return xLike
	case z >= 0 && v >= 7:
		return xOK
	}
	return 0
}

// signals — всё о человеке из своих таблиц и журнала.
func (s *Service) signals(ctx context.Context, owner string) (Signals, error) {
	sg := newSignals()
	rows, err := s.DB.R.QueryContext(ctx, `SELECT kp_id, score FROM rec_ratings WHERE owner = ?`, owner)
	if err != nil {
		return sg, err
	}
	for rows.Next() {
		var kp, v int
		rows.Scan(&kp, &v)
		sg.Ratings[kp] = v
	}
	rows.Close()
	rows, err = s.DB.R.QueryContext(ctx, `SELECT kp_id, mark FROM rec_marks WHERE owner = ?`, owner)
	if err != nil {
		return sg, err
	}
	for rows.Next() {
		var kp int
		var m string
		rows.Scan(&kp, &m)
		sg.Marks[kp] = m
	}
	rows.Close()
	rows, err = s.DB.R.QueryContext(ctx, `SELECT kp_id, at FROM rec_shown WHERE owner = ?`, owner)
	if err != nil {
		return sg, err
	}
	for rows.Next() {
		var kp int
		var at int64
		rows.Scan(&kp, &at)
		sg.Shown[kp] = time.UnixMilli(at)
	}
	rows.Close()
	if s.Journal != nil {
		views, err := s.Journal.Views(ctx, owner)
		if err != nil {
			return sg, err
		}
		sg.addViews(views)
	}
	if s.Wanted != nil {
		for _, kp := range s.Wanted(ctx, owner) {
			if _, ok := sg.Marks[kp]; !ok {
				sg.Marks[kp] = "want"
			}
		}
	}
	return sg, nil
}
