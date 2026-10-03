package playback

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode/utf16"

	"golang.org/x/text/encoding/charmap"
)

// packets — пакеты файла: «дорожка,время,размер» по строке.
func packets(t *testing.T, tl Tools, file string, extra ...string) []string {
	t.Helper()
	out, err := tl.output(context.Background(), tl.FFprobe, append(append([]string{"-v", "error", "-show_entries", "packet=stream_index,pts_time,size",
		"-of", "csv=p=0"}, extra...), file)...)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Fields(string(out))
}

func TestKeyframeFixture(t *testing.T) {
	tl := tools(t)
	in := filepath.Join("testdata", "sample.mkv")
	for _, c := range []struct{ at, want float64 }{{5.3, 4}, {6, 6}, {11.9, 10}, {0.5, 0}, {0, 0}} {
		if k, err := tl.Keyframe(context.Background(), in, 0, c.at); err != nil || k != c.want {
			t.Errorf("ключевой кадр до %v: %v %v, ждали %v", c.at, k, err, c.want)
		}
	}
}

// Поток с ключевого кадра 4 с: MKV начинается ровно с него (первый пакет — тот же, что в файле), звук — AAC
// стерео, субтитры сдвинуты на начало потока; TS — те же 200 кадров (с 4,00 по 11,96 с), звук рядом с картинкой.
func TestStreamFixture(t *testing.T) {
	tl := tools(t)
	in := filepath.Join("testdata", "sample.mkv")
	src := packets(t, tl, in, "-select_streams", "0", "-read_intervals", "4%+#1")
	run := func(o streamOpts) string {
		var b bytes.Buffer
		var in io.Reader
		if o.Sub != nil && o.Sub.File != "" {
			cues, err := tl.fileCues(context.Background(), o.Sub.File)
			if err != nil {
				t.Fatal(err)
			}
			var v bytes.Buffer
			writeVTT(&v, cues, seekAt(o.From), seekAt(o.From))
			in = &v
		}
		if err := tl.stream(context.Background(), streamArgs(o), in, &b); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(t.TempDir(), "out."+o.Format)
		os.WriteFile(p, b.Bytes(), 0o644)
		return p
	}
	mkv := run(streamOpts{Input: in, From: 4, Video: 0, Audio: &Track{ID: 1, Codec: "ac3", Channels: 6}, Sub: &Sub{ID: "3", Codec: "subrip"}, Format: "mkv"})
	var firstVideo, firstSub string
	for _, p := range packets(t, tl, mkv) {
		f := strings.Split(p, ",")
		if f[0] == "0" && firstVideo == "" {
			firstVideo = f[2]
		}
		if f[0] == "2" && firstSub == "" {
			firstSub = f[1]
		}
	}
	if want := strings.Split(src[0], ",")[2]; firstVideo != want {
		t.Errorf("первый кадр MKV: размер %s, у кадра 4 с — %s (%v)", firstVideo, want, src)
	}
	if s, _ := strconv.ParseFloat(firstSub, 64); s < 0.95 || s > 1.05 {
		t.Errorf("реплика с 5 с — в потоке на %s, ждали 1", firstSub)
	}
	info, _ := tl.output(context.Background(), tl.FFprobe, "-v", "error", "-select_streams", "a", "-show_entries", "stream=codec_name,channels", "-of", "csv=p=0", mkv)
	if strings.TrimSpace(string(info)) != "aac,2" {
		t.Errorf("звук MKV: %q", info)
	}
	ext := run(streamOpts{Input: in, From: 4, Video: 0, Audio: &Track{ID: 1, Codec: "ac3", Channels: 6},
		Sub: &Sub{ID: "f0", File: filepath.Join("testdata", "sample.rus.srt")}, Format: "mkv"})
	var v0e, s0e float64 = -1, -1
	for _, p := range packets(t, tl, ext) {
		f := strings.Split(p, ",")
		x, _ := strconv.ParseFloat(f[1], 64)
		if f[0] == "0" && v0e < 0 {
			v0e = x
		}
		if f[0] == "2" && s0e < 0 {
			s0e = x
		}
	}
	if v0e != 0 || s0e < 2.95 || s0e > 3.05 {
		t.Errorf("MKV с внешними: кадр 4 с — на %.3f (ждали 0), «Внешняя два» (7 с) — на %.3f (ждали 3)", v0e, s0e)
	}
	ts := run(streamOpts{Input: in, From: 4, Video: 0, Audio: &Track{ID: 1, Codec: "ac3", Channels: 6}, Format: "ts"})
	var nv int
	var v0, a0 float64 = -1, -1
	for _, p := range packets(t, tl, ts) {
		f := strings.Split(p, ",")
		x, _ := strconv.ParseFloat(f[1], 64)
		switch f[0] {
		case "0":
			nv++
			if v0 < 0 || x < v0 {
				v0 = x
			}
		case "1":
			if a0 < 0 {
				a0 = x
			}
		}
	}
	if nv != 200 {
		t.Errorf("кадров в TS: %d, ждали 200 (с ключевого кадра 4 с)", nv)
	}
	if d := a0 - v0; d < 0 || d > 0.3 {
		t.Errorf("звук от картинки: %.3f с (картинка %.3f, звук %.3f)", d, v0, a0)
	}
}

// Субтитры браузеру с ключевого кадра 4 с — во времени файла (начало потока вычтет плеер).
func TestSubsFixture(t *testing.T) {
	tl := tools(t)
	var b bytes.Buffer
	if err := tl.stream(context.Background(), subsArgs(filepath.Join("testdata", "sample.mkv"), 4, "3"), nil, &b); err != nil {
		t.Fatal(err)
	}
	if s := b.String(); !strings.Contains(s, "WEBVTT") || !strings.Contains(s, "05.000 -->") || !strings.Contains(s, "Вторая реплика") ||
		strings.Contains(s, "Первая") {
		t.Errorf("встроенные:\n%s", s)
	}
	cues, err := tl.fileCues(context.Background(), filepath.Join("testdata", "sample.rus.srt"))
	if err != nil {
		t.Fatal(err)
	}
	b.Reset()
	writeVTT(&b, cues, 4, 0)
	if s := b.String(); !strings.Contains(s, "00:00:07.000 --> 00:00:08.000\nВнешняя два") || strings.Contains(s, "Внешняя один") {
		t.Errorf("внешние:\n%s", s)
	}
}

// Русские .srt часто в cp1251, бывают в UTF-16 — ffmpeg без iconv их не перекодирует (ревью 18А, Important 3):
// файл читает сервер, ffmpeg получает UTF-8.
func TestFileCuesEncodings(t *testing.T) {
	tl := tools(t)
	src := "1\r\n00:00:07,000 --> 00:00:08,000\r\nВнешняя два\r\n"
	cp, err := charmap.Windows1251.NewEncoder().String(src)
	if err != nil {
		t.Fatal(err)
	}
	u16 := []byte{0xFF, 0xFE}
	for _, r := range utf16.Encode([]rune(src)) {
		u16 = append(u16, byte(r), byte(r>>8))
	}
	for name, data := range map[string][]byte{"cp1251.srt": []byte(cp), "utf16.srt": u16, "bom.srt": append([]byte("\xef\xbb\xbf"), src...)} {
		p := filepath.Join(t.TempDir(), name)
		os.WriteFile(p, data, 0o644)
		cues, err := tl.fileCues(context.Background(), p)
		if err != nil || len(cues) != 1 || cues[0].Text != "Внешняя два" || cues[0].Start != 7 {
			t.Errorf("%s: %+v %v", name, cues, err)
		}
	}
}

// Субтитры mov_text из MP4 (ревью 18А, Important 4): декодер в сборке есть, реплики приходят.
func TestMovTextSubs(t *testing.T) {
	tl := tools(t)
	var b bytes.Buffer
	if err := tl.stream(context.Background(), subsArgs(filepath.Join("testdata", "movtext.mp4"), 0, "1"), nil, &b); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "Вторая реплика") {
		t.Errorf("mov_text:\n%s", b.String())
	}
}
