// Package meta — метаданные раздач: разбор названий, Кинопоиск (рейтинги), картинки
// (спека, раздел 8). Модуль ratings — очередь запросов к Кинопоиску; остальное — библиотека.
package meta

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// Title — название раздачи по частям.
type Title struct {
	Ru      string   // первое название — обычно русское
	Orig    string   // первое название без кириллицы — обычно оригинальное; "" — такого нет
	Names   []string // все названия раздачи по порядку («Зверь», «Дикий драйв», «La fiera», …)
	Year    int      // 0 — год не найден
	Quality string   // «WEB-DL 1080p»; "" — не найдено
}

var (
	// Rutracker: «… [2014, Документальный, WEB-DL 1080p] …», год бывает диапазоном «2019-2020».
	reBracketYear = regexp.MustCompile(`\[((?:19|20)\d{2})(?:\s*-\s*\d{2,4})?(?:,([^\]]*))?\]`)
	// Rutor: «… (2014) WEB-DL 1080p от Группа», «(1999-2003)».
	reParenYear = regexp.MustCompile(`\(((?:19|20)\d{2})(?:\s*-\s*\d{2,4})?\)`)
	// Скобки внутри названий: режиссёр, «(1-5 серии из 5)», «[S01]», «[Обновлено]».
	reGroup = regexp.MustCompile(`\([^()]*\)|\[[^\[\]]*\]`)
	// Разделитель названий: « / » (у Rutracker бывает « \ »).
	reNameSep = regexp.MustCompile(`\s+[/\\]\s+`)
	// Части, которые не названия: «Сезон: 1», «Серии: 1-13 из 13».
	reSeasonPart = regexp.MustCompile(`(?i)^(сезон|серии|серия|season|episodes?)\b`)
	// Качество в скобке Rutracker — часть с типом рипа.
	reQuality = regexp.MustCompile(`(?i)\b(?:blu-?ray|bd-?remux|bd-?rip|hd-?dvd-?rip|hybridrip|web-?dl-?rip|web-?dl|web-?rip|hdtv-?rip|hdtv|hd-?rip|dvd-?rip|dvd-?\d|dvd|sat-?rip|dvb|iptv-?rip|tv-?rip|betacam-?rip|vhs-?rip|cam-?rip|dcp-?rip|remux)\b`)
)

// ParseTitle разбирает название раздачи обоих трекеров (спека, раздел 8):
//
//	Rutracker: «Русское / Original (Режиссёр) [2014, Документальный, WEB-DL 1080p] …»
//	Rutor:     «Русское / Original [S01] (2014) WEB-DL 1080p от Группа | …»
//
// Сезоны и серии («[S01]», «Сезон: 1 / Серии: 1-10 из 10», «(1-5 серий из 5)») не мешают.
func ParseTitle(s string) Title {
	s = strings.Join(strings.Fields(s), " ")
	var t Title
	names := s
	rt := reBracketYear.FindStringSubmatchIndex(s)
	ru := reParenYear.FindStringSubmatchIndex(s)
	switch {
	case rt != nil && (ru == nil || rt[0] < ru[0]):
		names = s[:rt[0]]
		t.Year, _ = strconv.Atoi(s[rt[2]:rt[3]])
		if rt[4] >= 0 {
			for _, part := range strings.Split(s[rt[4]:rt[5]], ",") {
				if part = strings.TrimSpace(part); reQuality.MatchString(part) {
					t.Quality = part
					break
				}
			}
		}
	case ru != nil:
		names = s[:ru[0]]
		t.Year, _ = strconv.Atoi(s[ru[2]:ru[3]])
		rest := s[ru[1]:]
		for _, sep := range []string{" от ", " | "} {
			if i := strings.Index(rest, sep); i >= 0 {
				rest = rest[:i]
			}
		}
		t.Quality = strings.TrimSpace(rest)
	}
	for {
		stripped := reGroup.ReplaceAllString(names, " ")
		if stripped == names {
			break
		}
		names = stripped
	}
	for _, part := range reNameSep.Split(strings.Join(strings.Fields(names), " "), -1) {
		part = strings.TrimSpace(part)
		if part == "" || reSeasonPart.MatchString(part) {
			continue
		}
		t.Names = append(t.Names, part)
		if t.Orig == "" && hasLetter(part) && !hasCyrillic(part) {
			t.Orig = part
		}
	}
	if len(t.Names) > 0 {
		t.Ru = t.Names[0]
	}
	return t
}

// NormTitle — название для сравнения и кэша: строчные буквы, «ё» как «е», без знаков.
func NormTitle(s string) string {
	s = strings.NewReplacer("ё", "е", "Ё", "Е").Replace(strings.ToLower(s))
	return strings.Join(strings.FieldsFunc(s, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }), " ")
}

func hasCyrillic(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Cyrillic, r) {
			return true
		}
	}
	return false
}

func hasLetter(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) {
			return true
		}
	}
	return false
}
