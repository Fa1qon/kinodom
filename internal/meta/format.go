package meta

import (
	"cmp"
	"maps"
	"regexp"
	"slices"
	"strings"
)

// formatByExt — формат (контейнер) видеофайла по расширению (спека этапа 7, раздел 10.2). Список — тот
// же, что у видеофайлов движка (тест torrents.TestEveryVideoTypeHasFormat).
var formatByExt = map[string]string{
	".mkv": "MKV", ".mp4": "MP4", ".m4v": "MP4", ".avi": "AVI", ".ts": "TS", ".m2ts": "M2TS",
	".mov": "MOV", ".wmv": "WMV", ".webm": "WEBM", ".mpg": "MPG", ".mpeg": "MPG",
}

// FileFormat — формат файла по расширению («MKV»); "" — не видео.
func FileFormat(name string) string {
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	i := strings.LastIndexByte(name, '.')
	if i < 0 {
		return ""
	}
	return formatByExt[strings.ToLower(name[i:])]
}

// File — файл раздачи: путь внутри раздачи и размер.
type File struct {
	Name string
	Size int64
}

// Format — формат раздачи по видеофайлам; несколько — через запятую по убыванию общего размера
// («AVI, MKV»); "" — видеофайлов нет.
func Format(files []File) string {
	total := map[string]int64{}
	for _, f := range files {
		if ff := FileFormat(f.Name); ff != "" {
			total[ff] += f.Size
		}
	}
	fs := slices.Collect(maps.Keys(total))
	slices.SortFunc(fs, func(a, b string) int { return cmp.Or(cmp.Compare(total[b], total[a]), cmp.Compare(a, b)) })
	return strings.Join(fs, ", ")
}

// reFormatLine — строка описания «Формат видео: MKV», «Формат: MKV», «Контейнер: MKV».
var reFormatLine = regexp.MustCompile(`(?i)(?:формат(?:\s+видео)?|контейнер)\s*:\s*([a-z0-9]+)`)

// FormatInText — формат из описания раздачи: первая строка «Формат видео: …», «Формат: …» или
// «Контейнер: …», где значение — формат видео. «Формат: AC3» у звука и «Формат: 16:9» пропускаются.
// "" — не найден.
func FormatInText(text string) string {
	for _, m := range reFormatLine.FindAllStringSubmatch(text, -1) {
		if f := formatByExt["."+strings.ToLower(m[1])]; f != "" {
			return f
		}
	}
	return ""
}
