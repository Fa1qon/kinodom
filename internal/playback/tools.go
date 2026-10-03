// Package playback — свой плеер фильмов (спека цикла 18): сведения о файле (ffprobe), поток с ключевого
// кадра — видео как есть, звук в AAC (ffmpeg), субтитры в WebVTT; не больше трёх плееров.
package playback

import (
	"os"
	"path/filepath"
)

// Tools — урезанные ffmpeg и ffprobe рядом с kinodom.exe (third_party/ffmpeg, план 18А).
type Tools struct {
	FFmpeg  string
	FFprobe string
}

// Find — ffmpeg.exe и ffprobe.exe в папке dir; false — хотя бы одного нет.
func Find(dir string) (Tools, bool) {
	t := Tools{FFmpeg: filepath.Join(dir, "ffmpeg.exe"), FFprobe: filepath.Join(dir, "ffprobe.exe")}
	for _, p := range []string{t.FFmpeg, t.FFprobe} {
		if st, err := os.Stat(p); err != nil || st.IsDir() {
			return Tools{}, false
		}
	}
	return t, true
}
