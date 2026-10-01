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
	Season  string   // сезон и серии: «S01», «Сезон: 1, Серии: 1-13 из 13»; "" — нет (спека этапа 7, раздел 10.4)
	Series  bool     // сериал: сезон, серии, «N из M», хвост «- Episode N» (спека 11b, 5.1)
}

// WorkKey — ключ произведения (спека 11b, 5.1 и 5.3): «название|год|f» или «…|s» (сериал); название —
// русское (иначе оригинальное) без регистра и знаков. Разные рипы одного фильма и сезоны одного года —
// один ключ; фильм и сериал с одним названием и годом — разные. "" — названия нет.
func WorkKey(t Title) string {
	name := t.Ru
	if name == "" {
		name = t.Orig
	}
	if name = NormTitle(name); name == "" {
		return ""
	}
	kind := "f"
	if t.Series {
		kind = "s"
	}
	return name + "|" + strconv.Itoa(t.Year) + "|" + kind
}

var (
	// Rutracker: «… [2014, Документальный, WEB-DL 1080p] …», год бывает диапазоном «2019-2020».
	reBracketYear = regexp.MustCompile(`\[((?:19|20)\d{2})(?:\s*-\s*\d{2,4})?(?:,([^\]]*))?\]`)
	// Rutor: «… (2014) WEB-DL 1080p от Группа», «(1999-2003)».
	reParenYear = regexp.MustCompile(`\(((?:19|20)\d{2})(?:\s*-\s*\d{2,4})?\)`)
	// reSlashYear — год отдельной частью через « / » (Kinozal, NNM-Club, Bitru: «Ru / Orig / 2024 / ДБ / WEB-DLRip»).
	reSlashYear = regexp.MustCompile(`\s+/\s+((?:19|20)\d{2})(?:\s+/\s+|$)`)
	// Скобки внутри названий: режиссёр, «(1-5 серии из 5)», «[S01]», «[Обновлено]».
	reGroup = regexp.MustCompile(`\([^()]*\)|\[[^\[\]]*\]`)
	// Разделитель названий: « / » (у Rutracker бывает « \ »).
	reNameSep = regexp.MustCompile(`\s+[/\\]\s+`)
	// Части, которые не названия: «Сезон: 1», «Серии: 1-13 из 13», «Season 5» — слово и номер; «Сезон
	// охоты» — название (\b в Go — только ASCII, кириллицу не ловит — хвост Х2).
	reSeasonPart = regexp.MustCompile(`(?i)^(?:сезон|серии|серия|season|episodes?)\s*:?\s*\d`)
	// Хвост названия «- Episode 3», «- Серия 3»: номер серии, не часть названия.
	reEpisodeTail = regexp.MustCompile(`(?i)\s+[-–—]\s+(?:episode|серия|эпизод)\s*\d+\s*$`)
	// Сезон и серии: «[S01]», «[S01-24]», «[S02E01-08]», «[02x13 из 13]», «Сезон: 1», «(1-5 серий из 5)»,
	// «Серии: 1-13 из 13».
	// «26 сезон: 10 серии» — сезон 26 (номер перед словом — раньше «сезон: N»), «[01-04 из 04]» — серии.
	reSeason = regexp.MustCompile(`(?i)\d{1,3}\s+(?:сезон|season)(?:[^\p{L}]|$)|\[\d{1,3}(?:\s*-\s*\d{1,3})?\s+из\s+\d{1,3}\]|\[S\d{1,2}(?:E\d{1,3})?(?:-S?E?\d{1,3})?\]|\[\d{1,2}x\d{1,3}(?:-\d{1,3})?(?:\s+из\s+\d+)?\]|(?:сезон|season)\s*:?\s*\d+(?:\s*-\s*\d+)?|\d+(?:\s*-\s*\d+)?\s+сери[йияю]\s+из\s+\d+|(?:серии|серия)\s*:?\s*\d+(?:\s*-\s*\d+)?(?:\s+из\s+\d+)?`)
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
	sy := reSlashYear.FindStringSubmatchIndex(s)
	switch {
	case sy != nil && (rt == nil || sy[0] < rt[0]) && (ru == nil || sy[0] < ru[0]):
		// «Ru / Orig / 2024 / озвучка / качество»: названия — до года, качество — часть после него.
		names = s[:sy[0]]
		t.Year, _ = strconv.Atoi(s[sy[2]:sy[3]])
		for _, part := range reNameSep.Split(s[sy[3]:], -1) {
			if q := reQuality.FindString(part); q != "" {
				t.Quality = q
				break
			}
		}
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
		if tail := reEpisodeTail.FindStringIndex(part); tail != nil {
			part, t.Series = part[:tail[0]], true
		}
		t.Names = append(t.Names, part)
		if t.Orig == "" && hasLetter(part) && !hasCyrillic(part) {
			t.Orig = part
		}
	}
	if len(t.Names) > 0 {
		t.Ru = t.Names[0]
	}
	var seasons []string
	for _, m := range reSeason.FindAllString(s, -1) {
		seasons = append(seasons, strings.TrimRight(strings.Trim(m, "[]"), " :,;"))
	}
	t.Season = strings.Join(seasons, ", ")
	t.Series = t.Series || t.Season != ""
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
