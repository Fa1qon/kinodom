package library

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// videoExt — видео медиатеки (спека, раздел 5.2); аудио нет.
var videoExt = map[string]bool{".mkv": true, ".mp4": true, ".avi": true, ".m4v": true, ".mov": true, ".wmv": true, ".mpg": true,
	".mpeg": true, ".ts": true, ".m2ts": true, ".webm": true, ".flv": true, ".vob": true, ".3gp": true}

func isVideo(name string) bool { return videoExt[strings.ToLower(filepath.Ext(name))] }

// Parsed — название из имени папки или файла.
type Parsed struct {
	Title string
	Year  int
	KP    int // из метки [kp12345]; 0 — нет
}

var (
	reKPTag = regexp.MustCompile(`(?i)\[kp(\d+)\]`)
	reYear  = regexp.MustCompile(`\b(19\d\d|20\d\d)\b`)
	// reCut — начало хвоста раздачи: сезон, «(Season …)», качество, источник, кодеки, группы озвучки.
	reCut = regexp.MustCompile(`(?i)\(\s*(?:season|сезон)|\bS\d{1,2}(?:\s?E\d{1,3})?\b|\bseason\b|` +
		`\b(?:web-?dl(?:rip)?|web-?rip|bd-?rip|br-?rip|blu-?ray|hd-?rip|dvd-?rip|hdtv(?:rip)?|tv-?rip|remux|uhd|2160p|1080[pi]?|720p|480p|4k|` +
		`amzn|nf|hmax|dsnp|atvp|mvo|dvo|avo|h 26[45]|x26[45]|hevc|avc|ac3|aac|ddp5|lostfilm|newstudio)\b|\[`)
	reSpaces = regexp.MustCompile(`\s+`)
)

// ParseName — название и год из имени папки или файла (спека, раздел 5.4): точки и подчёркивания —
// в пробелы; год — последний похожий на год (у «Бегущий по лезвию 2049 2017» — 2017); всё начиная с
// года, сезона, качества и источника отбрасывается.
func ParseName(name string) Parsed {
	var p Parsed
	if isVideo(name) {
		name = strings.TrimSuffix(name, filepath.Ext(name))
	}
	if m := reKPTag.FindStringSubmatch(name); m != nil {
		p.KP, _ = strconv.Atoi(m[1])
		name = reKPTag.ReplaceAllString(name, " ")
	}
	s := strings.NewReplacer(".", " ", "_", " ").Replace(name)
	cut := len(s)
	if loc := reCut.FindStringIndex(s); loc != nil {
		cut = loc[0]
	}
	if ys := reYear.FindAllStringSubmatchIndex(s, -1); ys != nil {
		y := ys[len(ys)-1]
		p.Year, _ = strconv.Atoi(s[y[2]:y[3]])
		cut = min(cut, y[0])
	}
	p.Title = clean(s[:cut])
	if p.Title == "" { // имя — только год («1917»): это название
		p.Title, p.Year = clean(s), 0
		if loc := reCut.FindStringIndex(p.Title); loc != nil && loc[0] > 0 {
			p.Title = clean(p.Title[:loc[0]])
		}
	}
	return p
}

func clean(s string) string {
	return strings.Trim(reSpaces.ReplaceAllString(s, " "), " -–—([,")
}

var (
	reSeasonOnly = []*regexp.Regexp{
		regexp.MustCompile(`(?i)^s\s?(\d{1,2})$`),
		regexp.MustCompile(`(?i)^season[ ._-]*(\d{1,2})$`),
		regexp.MustCompile(`(?i)^сезон[ ._-]*(\d{1,2})$`),
		regexp.MustCompile(`(?i)^(\d{1,2})[ ._-]*сезон$`),
	}
	reSeasonIn = []*regexp.Regexp{
		regexp.MustCompile(`(?i)\(\s*(?:season|сезон)[ ._-]*(\d{1,2})`),
		regexp.MustCompile(`(?i)\bS(\d{1,2})\b`),
		regexp.MustCompile(`(?i)(?:season|сезон)[ ._-]*(\d{1,2})`),
		regexp.MustCompile(`(?i)(\d{1,2})[ ._-]*сезон`),
	}
)

func firstNum(res []*regexp.Regexp, s string) (int, bool) {
	for _, re := range res {
		if m := re.FindStringSubmatch(s); m != nil {
			n, _ := strconv.Atoi(m[1])
			return n, true
		}
	}
	return 0, false
}

// SeasonOnly — имя папки — только сезон («S01», «Season 1», «Сезон 1», «1 сезон»): так опознаётся
// папка, которая сама сериал (спека, раздел 5.2). Имя раздачи с «S01» — не сезон: у «Сериалов» такие
// подпапки — отдельные сериалы.
func SeasonOnly(name string) (int, bool) { return firstNum(reSeasonOnly, strings.TrimSpace(name)) }

// SeasonDir — номер сезона папки внутри единицы: «S01», «… (Season 1) …», «Better.Call.Saul.S02…».
func SeasonDir(name string) (int, bool) {
	if n, ok := SeasonOnly(name); ok {
		return n, true
	}
	return firstNum(reSeasonIn, name)
}

var (
	reSxE     = regexp.MustCompile(`(?i)\bS(\d{1,2})[ ._-]*E(\d{1,3})\b`)
	reNxN     = regexp.MustCompile(`(?i)\b(\d{1,2})x(\d{2,3})\b`)
	reEpWord  = regexp.MustCompile(`(?i)сери[яи][ ._-]*(\d{1,3})|(\d{1,3})[ ._-]*сери[яи]`)
	reEpAlone = regexp.MustCompile(`(?i)\bE(\d{1,3})\b`)
)

// Episode — сезон и серия из имени файла; 0 — не указаны.
func Episode(file string) (season, episode int) {
	if isVideo(file) {
		file = strings.TrimSuffix(file, filepath.Ext(file))
	}
	for _, re := range []*regexp.Regexp{reSxE, reNxN} {
		if m := re.FindStringSubmatch(file); m != nil {
			season, _ = strconv.Atoi(m[1])
			episode, _ = strconv.Atoi(m[2])
			return season, episode
		}
	}
	if m := reEpWord.FindStringSubmatch(file); m != nil {
		episode, _ = strconv.Atoi(m[1] + m[2])
		return 0, episode
	}
	if m := reEpAlone.FindStringSubmatch(file); m != nil {
		episode, _ = strconv.Atoi(m[1])
	}
	return 0, episode
}

// Norm — название для сравнения: регистр, ё/е, пунктуация и лишние пробелы не важны.
func Norm(s string) string {
	s = strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(s, "ё", "е"), "Ё", "Е"))
	s = strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return r
		}
		return ' '
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

func hasCyrillic(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Cyrillic, r) {
			return true
		}
	}
	return false
}

// Variants — как искать название на Кинопоиске: как есть, а для латиницы — ещё обратный транслит
// в кириллицу (русские раздачи часто подписаны транслитом): без мягкого знака, с ним на конце слов
// на «т» («byt» → «быть») и на конце слов на т, з, н, л, с, д («Zhizn» → «жизнь»). Не больше пяти.
func Variants(title string) []string {
	out := []string{title}
	if hasCyrillic(title) {
		return out
	}
	words := strings.Fields(strings.ToLower(title))
	base := make([]string, len(words))
	for i, w := range words {
		base[i] = translitWord(w)
	}
	soft := func(ends string) string {
		v := make([]string, len(base))
		for i, w := range base {
			r := []rune(w)
			if len(r) >= 3 && strings.ContainsRune(ends, r[len(r)-1]) {
				w += "ь"
			}
			v[i] = w
		}
		return strings.Join(v, " ")
	}
	for _, v := range []string{strings.Join(base, " "), soft("т"), soft("тзнлсд")} {
		if v != "" && !contains(out, v) {
			out = append(out, v)
		}
	}
	return out
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

var translitMulti = []struct{ lat, cyr string }{
	{"shch", "щ"}, {"sch", "щ"}, {"zh", "ж"}, {"kh", "х"}, {"ts", "ц"}, {"ch", "ч"}, {"sh", "ш"},
	{"yu", "ю"}, {"ju", "ю"}, {"ya", "я"}, {"ja", "я"}, {"yo", "ё"}, {"jo", "ё"},
}

var translitOne = map[byte]string{'a': "а", 'b': "б", 'v': "в", 'g': "г", 'd': "д", 'e': "е", 'z': "з", 'i': "и", 'j': "й",
	'k': "к", 'l': "л", 'm': "м", 'n': "н", 'o': "о", 'p': "п", 'r': "р", 's': "с", 't': "т", 'u': "у", 'f': "ф", 'h': "х",
	'c': "ц", 'w': "в", 'x': "кс", 'q': "к", '\'': "ь"}

func isVowel(b byte) bool { return strings.IndexByte("aeiouy", b) >= 0 }

// translitWord — латинское слово (нижний регистр) в кириллицу по обычному транслиту раздач.
func translitWord(w string) string {
	tail := ""
	for _, e := range [][2]string{{"yi", "ый"}, {"yy", "ый"}, {"iy", "ий"}, {"ii", "ий"}} {
		if len(w) > 3 && strings.HasSuffix(w, e[0]) {
			w, tail = strings.TrimSuffix(w, e[0]), e[1]
			break
		}
	}
	var b strings.Builder
	for i := 0; i < len(w); {
		matched := false
		for _, m := range translitMulti {
			if strings.HasPrefix(w[i:], m.lat) {
				b.WriteString(m.cyr)
				i += len(m.lat)
				matched = true
				break
			}
		}
		if matched {
			continue
		}
		c := w[i]
		switch {
		case c == 'e' && i == 0:
			b.WriteString("э")
		case c == 'y' && i > 0 && isVowel(w[i-1]):
			b.WriteString("й")
		case c == 'y':
			b.WriteString("ы")
		default:
			if s, ok := translitOne[c]; ok {
				b.WriteString(s)
			} else {
				b.WriteByte(c)
			}
		}
		i++
	}
	return b.String() + tail
}
