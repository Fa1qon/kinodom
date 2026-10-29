package recs

import (
	"context"
	"math"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"kinodom/internal/history"
	"kinodom/internal/store"
)

// Journal — журнал просмотров (history.Service).
type Journal interface {
	Views(ctx context.Context, owner string) ([]history.View, error)
}

// Service — рекомендации.
type Service struct {
	DB      *store.DB
	Journal Journal
	// Wanted — «Смотреть позже» Kinodom человека номерами Кинопоиска; nil — без него.
	Wanted func(ctx context.Context, owner string) []int
	Now    func() time.Time

	model atomic.Pointer[Model]
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// SetModel ставит пакет модели (nil — выключить); прежний закрывается.
func (s *Service) SetModel(m *Model) {
	if old := s.model.Swap(m); old != nil && old != m {
		old.Close()
	}
}

// Model — текущий пакет; nil — не установлен.
func (s *Service) Model() *Model { return s.model.Load() }

// Query — чего хочется сейчас; пустой — «просто посоветуй».
type Query struct {
	Moods       []string `json:"moods,omitempty"`
	Genres      []string `json:"genres,omitempty"`
	NoGenres    []string `json:"noGenres,omitempty"`
	Kind        string   `json:"kind,omitempty"` // film | series | ""
	MaxDuration int      `json:"maxDuration,omitempty"`
	YearFrom    int      `json:"yearFrom,omitempty"`
	YearTo      int      `json:"yearTo,omitempty"`
	Like        []int    `json:"like,omitempty"` // «что-то вроде этих»
	Surprise    bool     `json:"surprise,omitempty"`
	Limit       int      `json:"limit,omitempty"`
}

// Rec — рекомендация с причинами.
type Rec struct {
	Film    Film               `json:"film"`
	Score   float64            `json:"score"`
	Because []int              `json:"because,omitempty"` // «потому что понравились …» — номера
	Moods   []string           `json:"moods,omitempty"`
	Members map[string]float64 `json:"members,omitempty"` // вместе: место у каждого (перцентиль)
}

// Параметры смешивания (kinosovet/DESIGN.md, проверены на живой модели).
const (
	popPenalty     = 0.15
	popPenaltySurp = 0.4
	moodShare      = 0.5
	moodGate       = 0.45
	zClip          = 3.0
	likeSeed       = 2.0
	dislikeLift    = 0.3
	shownPenalty   = 0.6
	shownWindow    = 10 * 24 * time.Hour
	candidatePool  = 1500
	nicheShare     = 0.3
	coldMinimum    = 3
	coldQuality    = 0.5 // вес качества при холодном старте
	groupMisery    = 0.30
)

type cand struct {
	cf      float64
	contrib [2]struct {
		kp int
		v  float64
	}
	mood  float64
	moods []string
}

func (c *cand) add(from int, v float64, liked bool) {
	c.cf += v
	if !liked || v <= c.contrib[1].v {
		return
	}
	if v > c.contrib[0].v {
		c.contrib[1] = c.contrib[0]
		c.contrib[0].kp, c.contrib[0].v = from, v
	} else {
		c.contrib[1].kp, c.contrib[1].v = from, v
	}
}

type scored struct {
	sg    Signals
	prof  Profile
	cands map[int]*cand
	cold  bool
}

func (s *Service) score(ctx context.Context, m *Model, owner string, q Query) (*scored, error) {
	sg, err := s.signals(ctx, owner)
	if err != nil {
		return nil, err
	}
	prof := sg.Build(s.now())
	for _, kp := range q.Like {
		prof[kp] = likeSeed
	}
	sc := &scored{sg: sg, prof: prof, cands: map[int]*cand{}}
	get := func(kp int) *cand {
		c := sc.cands[kp]
		if c == nil {
			c = &cand{}
			sc.cands[kp] = c
		}
		return c
	}
	var ids []int
	significant := 0
	for kp, x := range prof {
		if x != 0 {
			ids = append(ids, kp)
		}
		if math.Abs(x) >= 0.5 {
			significant++
		}
	}
	err = m.Neighbors(ctx, ids, func(from, to int, w float64) {
		v := prof[from] * w
		if prof[from] < 0 && v > 0 {
			v *= dislikeLift // «не любит X — значит, полюбит Y» — слабое основание
		}
		get(to).add(from, v, prof[from] > 0)
	})
	if err != nil {
		return nil, err
	}
	sc.cold = significant < coldMinimum && len(q.Like) == 0
	if len(q.Moods) > 0 {
		ms, err := m.MoodFilms(ctx, q.Moods, 600)
		if err != nil {
			return nil, err
		}
		for _, kp := range ms {
			get(kp)
		}
		all := make([]int, 0, len(sc.cands))
		for kp := range sc.cands {
			all = append(all, kp)
		}
		strength, err := m.Moods(ctx, all, q.Moods)
		if err != nil {
			return nil, err
		}
		for kp, axes := range strength {
			c := sc.cands[kp]
			for axis, v := range axes {
				c.mood += v / float64(len(q.Moods))
				if v >= 0.6 {
					c.moods = append(c.moods, axis)
				}
			}
			slices.Sort(c.moods)
		}
	}
	if sc.cold || len(sc.cands) < 50 {
		pop, err := m.Popular(ctx, 500)
		if err != nil {
			return nil, err
		}
		for _, kp := range pop {
			get(kp)
		}
	}
	return sc, nil
}

func pass(f Film, q Query) bool {
	if q.Kind != "" && f.Kind != q.Kind {
		return false
	}
	if q.MaxDuration > 0 && f.Kind == "film" && f.Duration > q.MaxDuration {
		return false
	}
	if q.YearFrom > 0 && f.Year > 0 && f.Year < q.YearFrom {
		return false
	}
	if q.YearTo > 0 && f.Year > q.YearTo {
		return false
	}
	g := strings.ToLower(f.Genres)
	if len(q.Genres) > 0 && !slices.ContainsFunc(q.Genres, func(x string) bool { return strings.Contains(g, strings.ToLower(x)) }) {
		return false
	}
	return !slices.ContainsFunc(q.NoGenres, func(x string) bool { return strings.Contains(g, strings.ToLower(x)) })
}

type ranked struct {
	film  Film
	c     *cand
	score float64
}

func (s *Service) rank(ctx context.Context, m *Model, sc *scored, q Query, exclude func(int) bool) ([]ranked, error) {
	ids := make([]int, 0, len(sc.cands))
	for kp := range sc.cands {
		if !sc.sg.Seen(kp) && !exclude(kp) && !slices.Contains(q.Like, kp) {
			ids = append(ids, kp)
		}
	}
	slices.SortFunc(ids, func(a, b int) int { return cmpDesc(sc.cands[a].cf, sc.cands[b].cf) })
	keep := ids[:0]
	for i, kp := range ids {
		if i < candidatePool || sc.cands[kp].mood > 0 || sc.cold {
			keep = append(keep, kp)
		}
	}
	fs, err := m.Films(ctx, keep)
	if err != nil {
		return nil, err
	}
	var out []ranked
	for _, kp := range keep {
		f, ok := fs[kp]
		if ok && pass(f, q) && (len(q.Moods) == 0 || sc.cands[kp].mood >= moodGate) {
			out = append(out, ranked{film: f, c: sc.cands[kp]})
		}
	}
	if len(out) == 0 {
		return out, nil
	}
	cf, pop, mood, quality := make([]float64, len(out)), make([]float64, len(out)), make([]float64, len(out)), make([]float64, len(out))
	for i, r := range out {
		cf[i], pop[i], mood[i] = r.c.cf, math.Log1p(r.film.Popularity), r.c.mood
		quality[i] = max(r.film.RatingKP, r.film.RatingIMDb) + 0.3*pop[i]
	}
	zcf, zpop, zmood := zscore(cf), zscore(pop), zscore(mood)
	if sc.cold { // мало сигналов: к похожести — качество фильма (не вместо: одна оценка «10» не теряется)
		zq := zscore(quality)
		for i := range zcf {
			zcf[i] += coldQuality * zq[i]
		}
	}
	if len(q.Moods) > 0 {
		zcf, zmood = clip(zcf), clip(zmood)
	}
	pp := popPenalty
	if q.Surprise {
		pp = popPenaltySurp
	}
	now := s.now()
	for i := range out {
		base := zcf[i]
		if len(q.Moods) > 0 {
			base = (1-moodShare)*zcf[i] + moodShare*zmood[i]
		}
		v := base - pp*zpop[i]
		if at, ok := sc.sg.Shown[out[i].film.KP]; ok && now.Sub(at) < shownWindow && sc.sg.Marks[out[i].film.KP] != "want" {
			v -= shownPenalty
		}
		out[i].score = v
	}
	slices.SortStableFunc(out, func(a, b ranked) int { return cmpDesc(a.score, b.score) })
	return out, nil
}

// diversify — лучшие, но не меньше nicheShare выдачи — вне 1000 самых популярных, если у нишевого есть
// основание и оно в первых 2×limit.
func diversify(list []ranked, limit int, line float64) []ranked {
	need := int(math.Ceil(nicheShare * float64(limit)))
	var out, rest []ranked
	niche := 0
	for i, r := range list {
		if len(out) == limit {
			break
		}
		isNiche := i < 2*limit && r.film.Popularity < line && (r.c.contrib[0].v > 0 || r.c.mood > 0)
		if !isNiche && limit-len(out) <= need-niche {
			rest = append(rest, r)
			continue
		}
		out = append(out, r)
		if isNiche {
			niche++
		}
	}
	for _, r := range rest {
		if len(out) == limit {
			break
		}
		out = append(out, r)
	}
	slices.SortStableFunc(out, func(a, b ranked) int { return cmpDesc(a.score, b.score) })
	return out
}

func toRec(r ranked) Rec {
	rec := Rec{Film: r.film, Score: r.score, Moods: r.c.moods}
	for _, ct := range r.c.contrib {
		if ct.kp != 0 && ct.v > 0 {
			rec.Because = append(rec.Because, ct.kp)
		}
	}
	return rec
}

// Recommend — подборка человеку.
func (s *Service) Recommend(ctx context.Context, owner string, q Query) ([]Rec, error) {
	m := s.Model()
	if m == nil {
		return nil, ErrNoModel
	}
	if q.Limit <= 0 {
		q.Limit = 12
	}
	sc, err := s.score(ctx, m, owner, q)
	if err != nil {
		return nil, err
	}
	list, err := s.rank(ctx, m, sc, q, func(int) bool { return false })
	if err != nil {
		return nil, err
	}
	var out []Rec
	for _, r := range diversify(list, q.Limit, m.niche) {
		out = append(out, toRec(r))
	}
	return out, nil
}

// Group — что посмотреть вместе: перцентиль места у каждого; у кого-то ниже порога или модель прямо ждёт,
// что не понравится, — отпадает; остальные — 0.5·среднее + 0.5·геометрическое.
func (s *Service) Group(ctx context.Context, owners []string, q Query) ([]Rec, error) {
	m := s.Model()
	if m == nil {
		return nil, ErrNoModel
	}
	if q.Limit <= 0 {
		q.Limit = 12
	}
	var all []*scored
	for _, o := range owners {
		sc, err := s.score(ctx, m, o, q)
		if err != nil {
			return nil, err
		}
		all = append(all, sc)
	}
	seenByAny := func(kp int) bool {
		return slices.ContainsFunc(all, func(sc *scored) bool { return sc.sg.Seen(kp) })
	}
	for _, sc := range all {
		for _, o := range all {
			for kp := range o.cands {
				if sc.cands[kp] == nil {
					sc.cands[kp] = &cand{}
				}
			}
		}
	}
	pct := make([]map[int]float64, len(all))
	var first map[int]ranked
	for i, sc := range all {
		list, err := s.rank(ctx, m, sc, q, seenByAny)
		if err != nil {
			return nil, err
		}
		pct[i] = map[int]float64{}
		n := float64(len(list))
		for j, r := range list {
			if r.c.cf < 0 {
				continue
			}
			pct[i][r.film.KP] = 1 - float64(j)/n
		}
		if i == 0 {
			first = map[int]ranked{}
			for _, r := range list {
				first[r.film.KP] = r
			}
		}
	}
	var out []ranked
	for kp, r := range first {
		sum, logSum, ok := 0.0, 0.0, true
		for i := range all {
			p, has := pct[i][kp]
			if !has || p < groupMisery {
				ok = false
				break
			}
			sum += p
			logSum += math.Log(p)
		}
		if ok {
			n := float64(len(all))
			r.score = 0.5*sum/n + 0.5*math.Exp(logSum/n)
			out = append(out, r)
		}
	}
	slices.SortFunc(out, func(a, b ranked) int {
		if c := cmpDesc(a.score, b.score); c != 0 {
			return c
		}
		return a.film.KP - b.film.KP
	})
	var recs []Rec
	for _, r := range diversify(out, q.Limit, m.niche) {
		rec := toRec(r)
		rec.Because = nil
		rec.Members = map[string]float64{}
		for i, o := range owners {
			rec.Members[o] = pct[i][r.film.KP]
		}
		recs = append(recs, rec)
	}
	return recs, nil
}

func zscore(xs []float64) []float64 {
	n := float64(len(xs))
	var mean, v float64
	for _, x := range xs {
		mean += x
	}
	mean /= n
	for _, x := range xs {
		v += (x - mean) * (x - mean)
	}
	sd := math.Sqrt(v / n)
	out := make([]float64, len(xs))
	if sd == 0 {
		return out
	}
	for i, x := range xs {
		out[i] = (x - mean) / sd
	}
	return out
}

func clip(xs []float64) []float64 {
	for i, x := range xs {
		xs[i] = min(max(x, -zClip), zClip)
	}
	return xs
}

func cmpDesc(a, b float64) int {
	switch {
	case a > b:
		return -1
	case a < b:
		return 1
	}
	return 0
}

// ---- отметки человека ----

// Rate — оценка 1..10.
func (s *Service) Rate(ctx context.Context, owner string, kp, score int, source string) error {
	_, err := s.DB.W.ExecContext(ctx, `INSERT INTO rec_ratings (owner, kp_id, score, source, at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(owner, kp_id) DO UPDATE SET score = excluded.score, source = excluded.source, at = excluded.at`, owner, kp, score, source, s.now().UnixMilli())
	return err
}

// Mark — notmine | seen | want; "" — снять.
func (s *Service) Mark(ctx context.Context, owner string, kp int, mark string) error {
	if mark == "" {
		_, err := s.DB.W.ExecContext(ctx, `DELETE FROM rec_marks WHERE owner = ? AND kp_id = ?`, owner, kp)
		return err
	}
	_, err := s.DB.W.ExecContext(ctx, `INSERT INTO rec_marks (owner, kp_id, mark, at) VALUES (?, ?, ?, ?)
		ON CONFLICT(owner, kp_id) DO UPDATE SET mark = excluded.mark, at = excluded.at`, owner, kp, mark, s.now().UnixMilli())
	return err
}

// MarkShown — эти рекомендации человек увидел.
func (s *Service) MarkShown(ctx context.Context, owner string, kps []int) error {
	now := s.now().UnixMilli()
	for _, kp := range kps {
		if _, err := s.DB.W.ExecContext(ctx, `INSERT INTO rec_shown (owner, kp_id, at) VALUES (?, ?, ?)
			ON CONFLICT(owner, kp_id) DO UPDATE SET at = excluded.at`, owner, kp, now); err != nil {
			return err
		}
	}
	return nil
}
