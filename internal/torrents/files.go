package torrents

import (
	"regexp"
	"sort"
	"strings"
)

// videoTypes — расширения видео и Content-Type для потока.
var videoTypes = map[string]string{
	".mkv": "video/x-matroska", ".mp4": "video/mp4", ".m4v": "video/mp4",
	".avi": "video/x-msvideo", ".ts": "video/mp2t", ".m2ts": "video/mp2t",
	".mov": "video/quicktime", ".wmv": "video/x-ms-wmv", ".webm": "video/webm",
	".mpg": "video/mpeg", ".mpeg": "video/mpeg",
}

// FileInfo — файл раздачи для клиента.
type FileInfo struct {
	Index int    `json:"index"`
	Name  string `json:"name"` // путь внутри раздачи через «/»
	Size  int64  `json:"size"`
}

// baseName — последняя часть пути (раздачи пишут пути через «/», но бывает и «\»).
func baseName(p string) string {
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		return p[i+1:]
	}
	return p
}

// extOf — расширение в нижнем регистре, с точкой.
func extOf(p string) string {
	b := baseName(p)
	if i := strings.LastIndexByte(b, '.'); i >= 0 {
		return strings.ToLower(b[i:])
	}
	return ""
}

var sampleWord = regexp.MustCompile(`(?i)(^|[^\p{L}])sample([^\p{L}]|$)`)

// isSample — в имени файла есть отдельное слово «sample».
func isSample(name string) bool { return sampleWord.MatchString(baseName(name)) }

// playableFiles — что показывать человеку в списке серий: видео без «sample» и без
// крошечных файлов (меньше 5 % самого большого видео), в порядке серий.
func playableFiles(all []FileInfo) []FileInfo {
	var videos []FileInfo
	var largest int64
	for _, f := range all {
		if _, ok := videoTypes[extOf(f.Name)]; !ok || isSample(f.Name) {
			continue
		}
		videos = append(videos, f)
		largest = max(largest, f.Size)
	}
	out := []FileInfo{}
	for _, f := range videos {
		if f.Size*20 >= largest {
			out = append(out, f)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return naturalLess(out[i].Name, out[j].Name) })
	return out
}

// naturalLess сравнивает строки «по-человечески»: числа внутри — как числа,
// поэтому «Серия 2» идёт раньше «Серия 10». Регистр не важен.
func naturalLess(a, b string) bool {
	a, b = strings.ToLower(a), strings.ToLower(b)
	for a != "" && b != "" {
		ca, ra := chunk(a)
		cb, rb := chunk(b)
		if ca != cb {
			if isDigits(ca) && isDigits(cb) {
				ta, tb := strings.TrimLeft(ca, "0"), strings.TrimLeft(cb, "0")
				if len(ta) != len(tb) {
					return len(ta) < len(tb)
				}
				if ta != tb {
					return ta < tb
				}
				return len(ca) < len(cb)
			}
			return ca < cb
		}
		a, b = ra, rb
	}
	return len(a) < len(b)
}

// chunk отделяет от начала строки кусок из одних цифр или из одних не-цифр.
// Цифры — однобайтовые в UTF-8, поэтому граница никогда не режет многобайтовую букву.
func chunk(s string) (string, string) {
	digit := isDigits(s)
	i := 1
	for i < len(s) && (s[i] >= '0' && s[i] <= '9') == digit {
		i++
	}
	return s[:i], s[i:]
}

func isDigits(s string) bool { return s != "" && s[0] >= '0' && s[0] <= '9' }
