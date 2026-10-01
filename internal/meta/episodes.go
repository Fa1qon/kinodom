package meta

import (
	"regexp"
	"strconv"
	"strings"
)

// Форматы серий в названиях раздач (Х2): «[01-07 из 08]», «[1x01-06 из 08]», «Серии: 1-8 из 10», «Серия 5
// из 10», «7 серий из 8», «S01E01-07», «Серии 1-4». Только ASCII-границы \b в Go — поэтому без них.
var (
	reEpRangeOf  = regexp.MustCompile(`(?:^|[^\d])(\d{1,3})\s*[-–]\s*(\d{1,3})\s+из\s+(\d{1,3})(?:[^\d]|$)`)
	reEpOneOf    = regexp.MustCompile(`сери[яи]\s*:?\s*(\d{1,3})\s+из\s+(\d{1,3})(?:[^\d]|$)`)
	reEpCountOf  = regexp.MustCompile(`(?:^|[^\d])(\d{1,3})\s+сери[йияю]\s+из\s+(\d{1,3})(?:[^\d]|$)`)
	reEpSxE      = regexp.MustCompile(`(?:^|[^\p{L}\d])s\d{1,2}e(\d{1,3})(?:\s*[-–]\s*(?:e)?(\d{1,3}))?(?:[^\d]|$)`)
	reEpRangeRaw = regexp.MustCompile(`сери[яи]\s*:?\s*(\d{1,3})\s*[-–]\s*(\d{1,3})(?:[^\d]|$)`)
)

// Episodes — серии из названия раздачи: from, to — вышедшие (0 — неизвестно), total — «из N» (0 —
// неизвестно). Для подписки на новые серии (спека 11b, 6.2): «из N» и все N есть — сериал вышел целиком.
func Episodes(title string) (from, to, total int) {
	s := strings.ToLower(title)
	n := func(v string) int { x, _ := strconv.Atoi(v); return x }
	if m := reEpRangeOf.FindStringSubmatch(s); m != nil && n(m[3]) >= n(m[2]) && n(m[2]) >= n(m[1]) {
		return n(m[1]), n(m[2]), n(m[3])
	}
	if m := reEpOneOf.FindStringSubmatch(s); m != nil {
		return n(m[1]), n(m[1]), n(m[2])
	}
	if m := reEpCountOf.FindStringSubmatch(s); m != nil && n(m[1]) > 0 {
		return 1, n(m[1]), n(m[2])
	}
	if m := reEpSxE.FindStringSubmatch(s); m != nil {
		from = n(m[1])
		to = from
		if m[2] != "" {
			to = n(m[2])
		}
		return from, to, 0
	}
	if m := reEpRangeRaw.FindStringSubmatch(s); m != nil {
		return n(m[1]), n(m[2]), 0
	}
	return 0, 0, 0
}
