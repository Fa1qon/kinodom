package playback

import (
	"path/filepath"
	"strconv"
	"strings"
)

// langCodes — язык из имени файла субтитров («Сериал.S01E01.rus.srt»).
var langCodes = map[string]string{"rus": "rus", "ru": "rus", "russian": "rus", "eng": "eng", "en": "eng", "english": "eng",
	"ukr": "ukr", "uk": "ukr", "ua": "ukr"}

// externalSubs — файлы субтитров рядом с видео: f0, f1… по порядку; язык — из имени.
func externalSubs(files []string) []Sub {
	out := make([]Sub, 0, len(files))
	for i, p := range files {
		name := filepath.Base(p)
		lang := ""
		parts := strings.Split(strings.ToLower(strings.TrimSuffix(name, filepath.Ext(name))), ".")
		for j := len(parts) - 1; j > 0 && lang == ""; j-- {
			lang = langCodes[parts[j]]
		}
		out = append(out, Sub{ID: "f" + strconv.Itoa(i), Lang: lang, Title: name, File: p})
	}
	return out
}
