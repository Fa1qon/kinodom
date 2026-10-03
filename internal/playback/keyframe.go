package playback

import (
	"context"
	"strconv"
	"strings"
)

// Keyframe — ближайший ключевой кадр видео video не позже at — по индексу файла, без чтения всего файла
// (спека 18, 3.3). at ≤ 0 — 0 без ffprobe. Кадр не нашёлся — ошибка (вызвавший берёт at).
func (t Tools) Keyframe(ctx context.Context, input string, video int, at float64) (float64, error) {
	if at <= 0 {
		return 0, nil
	}
	out, err := t.output(ctx, t.FFprobe, "-v", "error", "-select_streams", strconv.Itoa(video),
		"-show_entries", "packet=pts_time,flags", "-read_intervals", secs(at)+"%+#1", "-of", "csv=p=0", input)
	if err != nil {
		return 0, err
	}
	k, ok := parseKeyframe(out, at)
	if !ok {
		return 0, errNoKeyframe
	}
	return k, nil
}

var errNoKeyframe = errorString("ключевой кадр не нашёлся")

type errorString string

func (e errorString) Error() string { return string(e) }

// parseKeyframe — первый ключевой пакет не позже at из вывода ffprobe «время,флаги».
func parseKeyframe(out []byte, at float64) (float64, bool) {
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Split(strings.TrimSpace(line), ",")
		if len(f) < 2 || !strings.HasPrefix(f[1], "K") {
			continue
		}
		k, err := strconv.ParseFloat(f[0], 64)
		if err != nil || k > at+0.001 {
			continue
		}
		return max(k, 0), true
	}
	return 0, false
}

// secs — секунды для ffmpeg: 3 знака после точки.
func secs(f float64) string { return strconv.FormatFloat(f, 'f', 3, 64) }
