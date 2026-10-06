package playback

import (
	"slices"
	"strings"
	"testing"
)

// before — a встречается в args раньше b (оба есть).
func before(args []string, a, b string) bool {
	i, j := slices.Index(args, a), slices.Index(args, b)
	return i >= 0 && j >= 0 && i < j
}

func pair(args []string, k, v string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == k && args[i+1] == v {
			return true
		}
	}
	return false
}

func TestParseKeyframe(t *testing.T) {
	if k, ok := parseKeyframe([]byte("4.000000,K__\n"), 5.3); !ok || k != 4 {
		t.Errorf("кадр 4: %v %v", k, ok)
	}
	if _, ok := parseKeyframe([]byte("4.040000,___\n"), 5.3); ok {
		t.Error("не ключевой — принят")
	}
	if _, ok := parseKeyframe([]byte("7.000000,K__\n"), 5.3); ok {
		t.Error("кадр позже места — принят")
	}
	if _, ok := parseKeyframe(nil, 5.3); ok {
		t.Error("пусто — принят")
	}
}

func TestStreamArgs(t *testing.T) {
	ac3 := &Track{ID: 1, Codec: "ac3", Channels: 6}
	a := streamArgs(streamOpts{Input: "http://127.0.0.1:8090/media/1/f.mkv?own=1", From: 4, Video: 0, Audio: ac3, Format: "ts"})
	if !pair(a, "-ss", "4.150") || !before(a, "-ss", "-i") || !pair(a, "-readrate", "1") || !pair(a, "-readrate_initial_burst", "60") ||
		!pair(a, "-map", "0:0") || !pair(a, "-map", "0:1") || !pair(a, "-c:v", "copy") || !pair(a, "-c:a", "aac") || !pair(a, "-ac", "2") ||
		!pair(a, "-f", "mpegts") || a[len(a)-1] != "pipe:1" {
		t.Errorf("TS с 4 с, AC3: %v", a)
	}
	if a := streamArgs(streamOpts{Input: "x", From: 0, Video: 0, Audio: ac3, Format: "ts"}); slices.Contains(a, "-ss") {
		t.Errorf("с начала — без -ss: %v", a)
	}
	if a := streamArgs(streamOpts{Input: "x", Video: 0, Audio: &Track{ID: 2, Codec: "aac", Channels: 2}, Format: "ts"}); !pair(a, "-c:a", "copy") {
		t.Errorf("AAC стерео — как есть: %v", a)
	}
	if a := streamArgs(streamOpts{Input: "x", Video: 0, Audio: &Track{ID: 2, Codec: "aac", Channels: 6}, Format: "ts"}); !pair(a, "-c:a", "aac") {
		t.Errorf("AAC 5.1 — в стерео: %v", a)
	}
	a = streamArgs(streamOpts{Input: "x", From: 4, Video: 0, Audio: ac3, Sub: &Sub{ID: "3", Codec: "subrip"}, Format: "mkv"})
	if !pair(a, "-map", "0:3") || !pair(a, "-c:s", "copy") || !pair(a, "-f", "matroska") {
		t.Errorf("MKV со встроенными: %v", a)
	}
	a = streamArgs(streamOpts{Input: "x", From: 4, Video: 0, Audio: ac3, Sub: &Sub{ID: "f0", File: `D:\s.ass`}, Format: "mkv"})
	if strings.Count(strings.Join(a, " "), "-ss ") != 1 || !pair(a, "-i", "pipe:0") || !before(a, "webvtt", "pipe:0") || !pair(a, "-map", "1:0") ||
		!pair(a, "-c:s", "subrip") {
		t.Errorf("MKV с внешними — реплики сервер шлёт в stdin, уже сдвинутые: %v", a)
	}
	if a := streamArgs(streamOpts{Input: "x", Video: 0, Audio: ac3, Sub: &Sub{ID: "3", Codec: "mov_text"}, Format: "mkv"}); !pair(a, "-c:s", "subrip") {
		t.Errorf("mov_text — в SubRip (кодер srt в сборке не включён): %v", a)
	}
	if a := streamArgs(streamOpts{Input: "x", Video: 0, Audio: ac3, Sub: &Sub{ID: "3", Codec: "subrip"}, Format: "ts"}); pair(a, "-map", "0:3") {
		t.Errorf("TS — без субтитров (браузеру они идут отдельно): %v", a)
	}
	if a := streamArgs(streamOpts{Input: "x", From: 60, Video: 0, Audio: ac3, Format: "webm"}); !pair(a, "-ss", "60.150") ||
		!pair(a, "-c:v", "libvpx") || !pair(a, "-deadline", "realtime") || !pair(a, "-c:a", "libopus") ||
		!pair(a, "-f", "webm") || pair(a, "-c:v", "copy") {
		t.Errorf("WEBM — VP8 realtime и Opus вместо копии: %v", a)
	}
	if a := streamArgs(streamOpts{Input: "x", Video: 0, Format: "ts"}); slices.Contains(a, "-c:a") || strings.Count(strings.Join(a, " "), "-map") != 1 {
		t.Errorf("без звука: %v", a)
	}
}

// Встроенные субтитры браузеру — во времени файла (-copyts): с -ss ffmpeg сдвигал бы реплики, чтобы первая была
// с нуля (замер 2026-10-03: на 1 с), а начало потока плеер вычтет сам.
func TestSubsArgs(t *testing.T) {
	a := subsArgs("http://127.0.0.1:8090/media/1/f.mkv?own=1", 4, "3")
	if !pair(a, "-ss", "4.000") || !slices.Contains(a, "-copyts") || !pair(a, "-readrate", "1") || !pair(a, "-map", "0:3") ||
		!pair(a, "-c:s", "webvtt") || !pair(a, "-f", "webvtt") {
		t.Errorf("встроенные: %v", a)
	}
}

func TestVTT(t *testing.T) {
	cues := parseVTT([]byte("WEBVTT\n\n00:03.000 --> 00:04.000\nОдин\n\n01:02:03.500 --> 01:02:05.000 align:start\nДва\nстроки\n\nмусор\n"))
	if len(cues) != 2 || cues[0] != (cue{3, 4, "Один"}) || cues[1] != (cue{3723.5, 3725, "Два\nстроки"}) {
		t.Fatalf("разбор: %+v", cues)
	}
	var b strings.Builder
	writeVTT(&b, []cue{{1, 2, "до"}, {3.9, 5, "через"}, {7, 8, "после"}}, 4.15, 4.15)
	if s := b.String(); s != "WEBVTT\n\n00:00:00.000 --> 00:00:00.850\nчерез\n\n00:00:02.850 --> 00:00:03.850\nпосле\n\n" {
		t.Errorf("сдвиг на начало потока:\n%q", s)
	}
	b.Reset()
	writeVTT(&b, []cue{{1, 2, "до"}, {7, 8, "после"}}, 4, 0)
	if s := b.String(); s != "WEBVTT\n\n00:00:07.000 --> 00:00:08.000\nпосле\n\n" {
		t.Errorf("во времени файла:\n%q", s)
	}
}

// Ревью 18Б, C1: запас потока — по объёму, а не по секундам: на 40 Мбит/с минута запаса — 300 МБ, больше, чем MSE
// браузера держит (~150 МБ), и загрузка встаёт. Около 64 МБ, но не меньше 10 с и не больше 60 с.
func TestBurstFor(t *testing.T) {
	for _, c := range []struct {
		bits int64
		want int
	}{{0, 60}, {5_000_000, 60}, {20_000_000, 27}, {40_000_000, 13}, {100_000_000, 10}} {
		if got := burstFor(c.bits); got != c.want {
			t.Errorf("burstFor(%d) = %d, ждали %d", c.bits, got, c.want)
		}
	}
	if a := streamArgs(streamOpts{Input: "x", Video: 0, Format: "ts", Burst: 27}); !pair(a, "-readrate_initial_burst", "27") {
		t.Errorf("запас из сведений: %v", a)
	}
}
