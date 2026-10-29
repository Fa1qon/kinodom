package recs

import (
	"context"
	"math"
	"slices"
	"strings"
	"sync"
)

// Жанры. Любимый жанр — не тот, которого больше всего (драма есть почти у всего), а тот, чья доля среди
// понравившегося заметно выше его доли в каталоге: фантастика у любителя фантастики — в разы чаще, чем в
// кино вообще. Строки «По жанрам» — подборка, разложенная по жанрам, любимые первыми.

// GenreTitles — подписи строк по жанрам Кинопоиска.
var GenreTitles = map[string]string{
	"фантастика": "Фантастика", "фэнтези": "Фэнтези", "боевик": "Боевики", "детектив": "Детективы",
	"мелодрама": "Мелодрамы", "комедия": "Комедии", "триллер": "Триллеры", "драма": "Драмы",
	"криминал": "Криминал", "приключения": "Приключения", "ужасы": "Ужасы", "мультфильм": "Мультфильмы",
	"семейный": "Семейное", "военный": "Военные", "история": "Исторические", "биография": "Биографии",
	"документальный": "Документальное", "аниме": "Аниме", "вестерн": "Вестерны", "мюзикл": "Мюзиклы",
	"спорт": "Про спорт", "музыка": "Про музыку", "фильм-нуар": "Нуар", "детский": "Детское",
}

// GenreScore — насколько жанр «твой»: lift — во сколько раз его доля среди понравившегося выше, чем в
// каталоге (с поправкой на малое число фильмов); liked — сколько понравившихся фильмов с ним.
type GenreScore struct {
	Genre string  `json:"genre"`
	Title string  `json:"title"`
	Lift  float64 `json:"lift"`
	Liked int     `json:"liked"`
}

const (
	genreBaseTop   = 4000 // доля жанра в каталоге — по самым популярным фильмам пакета
	genreLikedConf = 6.0  // уверенность в жанре: n/(n+6) понравившихся фильмов
	genreMinBase   = 0.02 // доля редкого жанра в каталоге — не меньше: два «детских» фильма не делают жанр «твоим»
	genreRails     = 8    // строк «По жанрам»
	genreRailSize  = 12
	genreRankPool  = 900 // столько лучших кандидатов раскладывается по жанрам
)

// genres — жанры фильма из строки «драма, комедия».
func genres(f Film) []string {
	var out []string
	for _, g := range strings.Split(strings.ToLower(f.Genres), ",") {
		if g = strings.TrimSpace(g); g != "" {
			out = append(out, g)
		}
	}
	return out
}

var baseShareOnce sync.Map // *Model → map[string]float64

// baseShare — доля каждого жанра среди популярных фильмов пакета (считается один раз на пакет).
func (m *Model) baseShare(ctx context.Context) map[string]float64 {
	if v, ok := baseShareOnce.Load(m); ok {
		return v.(map[string]float64)
	}
	out := map[string]float64{}
	rows, err := m.db.QueryContext(ctx, `SELECT genres FROM films ORDER BY popularity DESC LIMIT ?`, genreBaseTop)
	if err != nil {
		return out
	}
	defer rows.Close()
	n := 0.0
	for rows.Next() {
		var g string
		rows.Scan(&g)
		for _, x := range genres(Film{Genres: g}) {
			out[x]++
		}
		n++
	}
	for g := range out {
		out[g] /= max(n, 1)
	}
	baseShareOnce.Store(m, out)
	return out
}

// GenreTaste — жанры человека по убыванию «своести».
func (s *Service) GenreTaste(ctx context.Context, owner string) ([]GenreScore, error) {
	m := s.Model()
	if m == nil {
		return nil, ErrNoModel
	}
	sg, err := s.signals(ctx, owner)
	if err != nil {
		return nil, err
	}
	prof := sg.Build(s.now())
	ids := make([]int, 0, len(prof))
	for kp, x := range prof {
		if x > 0 {
			ids = append(ids, kp)
		}
	}
	fs, err := s.films(ctx, m, ids)
	if err != nil {
		return nil, err
	}
	liked, count := map[string]float64{}, map[string]int{}
	total := 0.0
	for kp, f := range fs {
		x := prof[kp]
		total += x
		for _, g := range genres(f) {
			liked[g] += x
			count[g]++
		}
	}
	base := m.baseShare(ctx)
	var out []GenreScore
	for g, v := range liked {
		b := max(base[g], genreMinBase)
		if base[g] == 0 || total == 0 {
			continue
		}
		n := float64(count[g])
		lift := (v / total) / b * n / (n + genreLikedConf)
		out = append(out, GenreScore{Genre: g, Title: genreTitle(g), Lift: math.Round(lift*100) / 100, Liked: count[g]})
	}
	slices.SortFunc(out, func(a, b GenreScore) int { return cmpDesc(a.Lift, b.Lift) })
	return out, nil
}

func genreTitle(g string) string {
	if t, ok := GenreTitles[g]; ok {
		return t
	}
	if r := []rune(g); len(r) > 0 {
		return strings.ToUpper(string(r[:1])) + string(r[1:])
	}
	return g
}

// GenreRail — строка «По жанрам».
type GenreRail struct {
	Genre string `json:"genre"`
	Title string `json:"title"`
	Lift  float64
	Recs  []Rec
}

// GenreRails — подборка, разложенная по жанрам: сначала любимые (lift > 1), затем остальные заметные
// жанры подборки. Внутри строки — порядок подборки; фильм повторяется в соседней строке, только если иначе
// строка вышла бы короче половины.
func (s *Service) GenreRails(ctx context.Context, owner string, q Query) ([]GenreRail, []GenreScore, error) {
	taste, err := s.GenreTaste(ctx, owner)
	if err != nil {
		return nil, nil, err
	}
	q.Limit = genreRankPool
	list, err := s.ranked(ctx, owner, q)
	if err != nil {
		return nil, taste, err
	}
	order := []GenreScore{}
	for _, t := range taste {
		if t.Lift >= 1 {
			order = append(order, t)
		}
	}
	// жанры, которых нет среди любимых, — по тому, как часто они в подборке
	seen := map[string]bool{}
	for _, t := range order {
		seen[t.Genre] = true
	}
	freq := map[string]int{}
	for _, r := range list {
		for _, g := range genres(r.Film) {
			freq[g]++
		}
	}
	var rest []string
	for g, n := range freq {
		if !seen[g] && n >= 6 && GenreTitles[g] != "" {
			rest = append(rest, g)
		}
	}
	slices.SortFunc(rest, func(a, b string) int { return freq[b] - freq[a] })
	for _, g := range rest {
		order = append(order, GenreScore{Genre: g, Title: genreTitle(g)})
	}
	used := map[int]bool{}
	var rails []GenreRail
	for _, t := range order {
		if len(rails) == genreRails {
			break
		}
		var fresh, again []Rec
		for _, r := range list {
			if !slices.Contains(genres(r.Film), t.Genre) {
				continue
			}
			if used[r.Film.KP] {
				again = append(again, r)
			} else {
				fresh = append(fresh, r)
			}
		}
		row := fresh[:min(genreRailSize, len(fresh))]
		if len(row) < genreRailSize/2 {
			row = append(row, again[:min(genreRailSize-len(row), len(again))]...)
		}
		if len(row) < 4 {
			continue
		}
		for _, r := range row {
			used[r.Film.KP] = true
		}
		rails = append(rails, GenreRail{Genre: t.Genre, Title: t.Title, Lift: t.Lift, Recs: row})
	}
	return rails, taste, nil
}
