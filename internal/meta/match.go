package meta

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// seriesTypes — виды Кинопоиска для сериала.
var seriesTypes = map[string]bool{"TV_SERIES": true, "MINI_SERIES": true, "TV_SHOW": true}

// reSeasonNumber — номер сезона: «S02», «26 сезон», «Сезон: 2».
var reSeasonNumber = regexp.MustCompile(`(?i)S(\d{1,2})|(\d{1,3})\s+(?:сезон|season)|(?:сезон|season)\s*:?\s*(\d{1,3})`)

func seasonNumber(season string) int {
	m := reSeasonNumber.FindStringSubmatch(season)
	if m == nil {
		return 0
	}
	for _, g := range m[1:] {
		if n, err := strconv.Atoi(g); err == nil {
			return n
		}
	}
	return 0
}

// MatchKP — фильм Кинопоиска для раздачи t из найденного поиском без токена (спека 11b, 5.1;
// исследование 22.3). Год обязателен: у фильма ±1, у сериала — внутри годов выхода; сезон больше
// первого — не мини-сериал. Название «точно» (совпало с русским или оригинальным) или «вхождением»
// (все слова одного — в другом) и вид — не больше одной поблажки за раз; точные вытесняют поблажку.
// Несколько равных — берётся единственный с рейтингом (второй «такой же» без оценок — обычно
// малоизвестный тёзка), иначе номера нет: лучше без номера, чем чужой фильм (Х4). Раздача без года —
// только точно по обоим названиям и виду.
func MatchKP(hits []Film, t Title) (Film, bool) {
	var names []string
	for _, n := range t.Names {
		if n = NormTitle(n); n != "" {
			names = append(names, n)
		}
	}
	if len(names) == 0 {
		return Film{}, false
	}
	season := seasonNumber(t.Season)
	best, level := []Film(nil), 3
	for _, h := range hits {
		if season > 1 && h.Type == "MINI_SERIES" {
			continue
		}
		typeOK := seriesTypes[h.Type] == t.Series
		kp := []string{NormTitle(h.NameRu), NormTitle(h.NameOrig)}
		exact, soft := false, false
		for _, n := range names {
			for _, k := range kp {
				if k == "" {
					continue
				}
				exact = exact || n == k
				soft = soft || wordsWithin(n, k) || wordsWithin(k, n)
			}
		}
		var l int
		if t.Year == 0 {
			// Без года — только точно по обоим названиям раздачи (русскому и оригинальному) и виду.
			if t.Orig == "" || t.Orig == t.Ru || !typeOK ||
				!slices.Contains(kp, NormTitle(t.Ru)) || !slices.Contains(kp, NormTitle(t.Orig)) {
				continue
			}
		} else {
			if !yearFits(h, t.Year) || (!exact && !soft) {
				continue
			}
			if !exact {
				l++
			}
			if !typeOK {
				l++
			}
			if l > 1 {
				continue
			}
		}
		switch {
		case l < level:
			best, level = []Film{h}, l
		case l == level && !slices.ContainsFunc(best, func(b Film) bool { return b.ID == h.ID }):
			best = append(best, h)
		}
	}
	if len(best) == 1 {
		return best[0], true
	}
	var rated []Film
	for _, b := range best {
		if b.Rating > 0 {
			rated = append(rated, b)
		}
	}
	if len(rated) == 1 {
		return rated[0], true
	}
	return Film{}, false
}

// yearFits — год раздачи подходит фильму: у фильма ±1, у сериала — от года начала −1 до года конца +1
// (сериал идёт — без верхней границы).
func yearFits(h Film, year int) bool {
	if h.Year == 0 {
		return false
	}
	if !seriesTypes[h.Type] {
		return abs(h.Year-year) <= 1
	}
	return year >= h.Year-1 && (h.YearEnd == 0 || year <= h.YearEnd+1)
}

// wordsWithin — все слова a есть среди слов b.
func wordsWithin(a, b string) bool {
	wa, wb := strings.Fields(a), strings.Fields(b)
	if len(wa) == 0 || len(wa) >= len(wb) {
		return false
	}
	for _, w := range wa {
		if !slices.Contains(wb, w) {
			return false
		}
	}
	return true
}
