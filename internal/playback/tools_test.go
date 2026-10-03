package playback

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// tools — урезанные ffmpeg и ffprobe из third_party (план 18А); не на Windows — тест пропускается.
func tools(t *testing.T) Tools {
	t.Helper()
	tl, ok := Find(filepath.Join("..", "..", "third_party", "ffmpeg"))
	if !ok {
		if runtime.GOOS == "windows" {
			t.Fatal("нет third_party/ffmpeg/ffmpeg.exe и ffprobe.exe")
		}
		t.Skip("ffmpeg из third_party — только для Windows")
	}
	return tl
}

func TestFind(t *testing.T) {
	dir := t.TempDir()
	if _, ok := Find(dir); ok {
		t.Fatal("пустая папка — найдено")
	}
	os.WriteFile(filepath.Join(dir, "ffmpeg.exe"), nil, 0o644)
	if _, ok := Find(dir); ok {
		t.Fatal("без ffprobe — найдено")
	}
	os.WriteFile(filepath.Join(dir, "ffprobe.exe"), nil, 0o644)
	if tl, ok := Find(dir); !ok || tl.FFmpeg != filepath.Join(dir, "ffmpeg.exe") || tl.FFprobe != filepath.Join(dir, "ffprobe.exe") {
		t.Fatalf("оба есть: %+v %v", tl, ok)
	}
}

// Сборка в third_party — LGPL и с тем, что нужно плееру (спека 18, 3.1).
func TestBundledBuild(t *testing.T) {
	tl := tools(t)
	run := func(args ...string) string {
		out, err := exec.Command(tl.FFmpeg, append([]string{"-hide_banner"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %v %s", args, err, out)
		}
		return string(out)
	}
	if v := run("-version"); strings.Contains(v, "--enable-gpl") || strings.Contains(v, "--enable-nonfree") {
		t.Errorf("сборка не LGPL: %s", v)
	}
	for _, c := range []struct{ flag, want string }{
		{"-encoders", " aac "}, {"-encoders", " webvtt "}, {"-encoders", " subrip "},
		{"-decoders", " ac3 "}, {"-decoders", " eac3 "}, {"-decoders", " dca "}, {"-decoders", " truehd "},
		{"-muxers", " mpegts "}, {"-muxers", " matroska "}, {"-muxers", " webvtt "},
		{"-demuxers", " matroska,webm "}, {"-demuxers", " avi "}, {"-protocols", "http"},
	} {
		if !strings.Contains(run(c.flag), c.want) {
			t.Errorf("%s: нет %q", c.flag, c.want)
		}
	}
}
