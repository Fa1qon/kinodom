package playback

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// cue — реплика субтитров: время — секунды файла.
type cue struct {
	Start, End float64
	Text       string
}

// fileCues — реплики файла субтитров рядом с видео (SRT, ASS, VTT) — один проход ffmpeg, файл маленький.
func (t Tools) fileCues(ctx context.Context, path string) ([]cue, error) {
	out, err := t.output(ctx, t.FFmpeg, "-hide_banner", "-loglevel", "error", "-nostdin", "-i", path, "-map", "0:s:0",
		"-c:s", "webvtt", "-f", "webvtt", "pipe:1")
	if err != nil {
		return nil, err
	}
	return parseVTT(out), nil
}

// parseVTT — реплики WebVTT: блоки через пустую строку, строка времени «[ЧЧ:]ММ:СС.ммм --> [ЧЧ:]ММ:СС.ммм [настройки]».
func parseVTT(b []byte) []cue {
	var out []cue
	for _, block := range strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n\n") {
		lines := strings.Split(strings.Trim(block, "\n"), "\n")
		for i, l := range lines {
			from, to, ok := strings.Cut(l, " --> ")
			if !ok {
				continue
			}
			if f := strings.Fields(to); len(f) > 0 {
				to = f[0]
			}
			s, ok1 := vttTime(from)
			e, ok2 := vttTime(to)
			if ok1 && ok2 && i+1 < len(lines) {
				out = append(out, cue{s, e, strings.Join(lines[i+1:], "\n")})
			}
			break
		}
	}
	return out
}

// vttTime — «ММ:СС.ммм» или «ЧЧ:ММ:СС.ммм» в секундах.
func vttTime(s string) (float64, bool) {
	parts := strings.Split(strings.TrimSpace(s), ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, false
	}
	var t float64
	for _, p := range parts {
		v, err := strconv.ParseFloat(p, 64)
		if err != nil || v < 0 {
			return 0, false
		}
		t = t*60 + v
	}
	return t, true
}

// writeVTT — WebVTT из реплик, которые ещё не кончились к from; время — минус shift, начало — не раньше нуля.
func writeVTT(w io.Writer, cues []cue, from, shift float64) {
	fmt.Fprint(w, "WEBVTT\n\n")
	for _, c := range cues {
		if c.End <= from {
			continue
		}
		fmt.Fprintf(w, "%s --> %s\n%s\n\n", vttStamp(max(c.Start-shift, 0)), vttStamp(c.End-shift), c.Text)
	}
}

// vttStamp — секунды как «ЧЧ:ММ:СС.ммм».
func vttStamp(t float64) string {
	ms := int64(t*1000 + 0.5)
	return fmt.Sprintf("%02d:%02d:%02d.%03d", ms/3600000, ms/60000%60, ms/1000%60, ms%1000)
}
