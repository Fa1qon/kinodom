package m3u

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

var (
	reBrackets = regexp.MustCompile(`\[[^\]]*\]`)
	reParens   = regexp.MustCompile(`(?i)\((?:\d{3,4}[pi]|архив|not 24/7|geo-blocked)\)`)
	reShift    = regexp.MustCompile(`^\+[1-9]$`)
)

// quality — слова-метки качества: канал они не различают.
var quality = map[string]bool{
	"4k": true, "uhd": true, "fhd": true, "hd": true, "sd": true, "orig": true, "hdr": true,
	"2160p": true, "2160i": true, "1080p": true, "1080i": true, "720p": true, "720i": true,
	"576p": true, "576i": true, "480p": true, "480i": true, "360p": true, "50fps": true,
}

// Norm — название для сопоставления: нижний регистр, «ё» → «е», без пометок в квадратных скобках,
// без «(720p)», «(архив)», «(Not 24/7)», без меток качества и знаков, кроме «+». Сдвиг пишется
// одинаково: «(+4)», «+ 4» → «+4» (спека этапа 8, раздел 5.3).
func Norm(name string) string {
	n := strings.ReplaceAll(strings.ToLower(name), "ё", "е")
	n = reBrackets.ReplaceAllString(n, " ")
	n = reParens.ReplaceAllString(n, " ")
	ws := words(n)
	out := make([]string, 0, len(ws))
	for i := 0; i < len(ws); i++ {
		w := ws[i]
		switch {
		case quality[w]:
			continue
		case w == "fps":
			if len(out) > 0 && isNumber(out[len(out)-1]) { // «50 fps»
				out = out[:len(out)-1]
			}
			continue
		case w == "+" && i+1 < len(ws) && isNumber(ws[i+1]): // «+ 4»
			out = append(out, "+"+ws[i+1])
			i++
			continue
		case w == "+":
			continue
		}
		out = append(out, w)
	}
	return strings.Join(out, " ")
}

// words — части текста из букв, цифр и «+».
func words(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '+'
	})
}

func isNumber(s string) bool {
	_, err := strconv.Atoi(s)
	return err == nil
}

// SplitShift отделяет сдвиг «+N» (N от 1 до 9) в конце нормализованного названия: «первый канал +4» →
// «первый канал», 4. Нет сдвига — название как есть и 0.
func SplitShift(norm string) (string, int) {
	i := strings.LastIndexByte(norm, ' ')
	if i < 0 {
		return norm, 0
	}
	last := norm[i+1:]
	if !reShift.MatchString(last) {
		return norm, 0
	}
	return norm[:i], int(last[1] - '0')
}
