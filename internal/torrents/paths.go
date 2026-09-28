package torrents

import (
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
)

// maxComponentRunes — предел длины одной части пути. Названия раздач бывают на 300 символов,
// а Проводник и многие плееры спотыкаются о пути длиннее 260.
const maxComponentRunes = 100

var badChars = strings.NewReplacer("<", "_", ">", "_", ":", "_", `"`, "_", "/", "_", `\`, "_", "|", "_", "?", "_", "*", "_")

// reservedNames — имена устройств Windows: файл с таким именем создать нельзя.
var reservedNames = map[string]bool{
	"CON": true, "PRN": true, "AUX": true, "NUL": true,
	"COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true,
	"COM6": true, "COM7": true, "COM8": true, "COM9": true,
	"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true,
	"LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
}

// sanitizeComponent делает из имени файла или папки допустимое в Windows: заменяет
// запрещённые символы, обрезает длинное имя (сохраняя расширение), убирает точки
// и пробелы в конце и обходит имена устройств.
func sanitizeComponent(s string) string {
	s = badChars.Replace(s)
	s = strings.Map(func(r rune) rune {
		if r < 32 {
			return '_'
		}
		return r
	}, s)
	s = truncateKeepExt(s, maxComponentRunes)
	s = strings.TrimRight(s, " .")
	if s == "" {
		return "_"
	}
	stem, _, _ := strings.Cut(s, ".")
	if reservedNames[strings.ToUpper(strings.TrimSpace(stem))] {
		s = "_" + s
	}
	return s
}

// truncateKeepExt обрезает имя до max символов, сохраняя расширение (если оно не длиннее 10).
func truncateKeepExt(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	ext := filepath.Ext(s)
	if utf8.RuneCountInString(ext) > 10 {
		ext = ""
	}
	stem := []rune(strings.TrimSuffix(s, ext))
	keep := max - utf8.RuneCountInString(ext)
	return strings.TrimRight(string(stem[:keep]), " .") + ext
}

// torrentDir — папка раздачи: «<название> [<первые 8 символов infohash>]». Хэш в имени
// не даёт двум разным раздачам с одинаковым названием писать в одну папку.
func torrentDir(base string, info *metainfo.Info, ih metainfo.Hash) string {
	return filepath.Join(base, fmt.Sprintf("%s [%s]", sanitizeComponent(info.BestName()), ih.HexString()[:8]))
}

// filePath — путь файла внутри папки раздачи; у однофайловой раздачи — её название.
func filePath(o storage.FilePathMakerOpts) string {
	parts := o.File.BestPath()
	if len(parts) == 0 {
		return sanitizeComponent(o.Info.BestName())
	}
	out := make([]string, len(parts))
	for i, p := range parts {
		out[i] = sanitizeComponent(p)
	}
	return filepath.Join(out...)
}

// enginePath — тот же полный путь к файлу, который вычислит файловое хранилище движка.
func enginePath(base string, info *metainfo.Info, ih metainfo.Hash, fi metainfo.FileInfo) string {
	return filepath.Join(torrentDir(base, info, ih), filePath(storage.FilePathMakerOpts{Info: info, File: &fi}))
}
