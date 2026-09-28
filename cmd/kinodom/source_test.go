package main

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"kinodom/internal/source/rutor/rutortest"
)

func runSource(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	code = runCLI(append([]string{"source", "rutor"}, args...), &out, &errb)
	return code, out.String(), errb.String()
}

func TestSourceRutorTop(t *testing.T) {
	s := rutortest.NewServer(t)
	code, out, errOut := runSource(t, "top", "--mirror", s.Mirror.URL, "--limit", "3", "12")
	if code != 0 {
		t.Fatalf("код %d: %s", code, errOut)
	}
	for _, want := range []string{"Найдено 100 раздач", "Динозавры / The Dinosaurs", "[1077013]", "ответило зеркало " + s.Mirror.URL} {
		if !strings.Contains(out, want) {
			t.Errorf("в выводе нет %q:\n%s", want, out)
		}
	}
	if rows := regexp.MustCompile(`(?m)\[\d+\]$`).FindAllString(out, -1); len(rows) != 3 {
		t.Errorf("строк раздач %d, нужно 3:\n%s", len(rows), out)
	}
}

func TestSourceRutorDetailsAndTorrent(t *testing.T) {
	s := rutortest.NewServer(t)
	code, out, errOut := runSource(t, "details", "--mirror", s.Mirror.URL, "--download", s.Download.URL, "1077013")
	if code != 0 || !strings.Contains(out, "Кинопоиск: 5898244") || !strings.Contains(out, "раздают 67") {
		t.Fatalf("details: код %d\n%s\n%s", code, out, errOut)
	}
	file := filepath.Join(t.TempDir(), "раздача.torrent")
	code, out, errOut = runSource(t, "torrent", "--mirror", s.Mirror.URL, "--download", s.Download.URL, "1077013", file)
	if code != 0 {
		t.Fatalf("torrent: код %d\n%s\n%s", code, out, errOut)
	}
	got, err := os.ReadFile(file)
	if err != nil || !bytes.Equal(got, rutortest.Page(t, "download_1077013.torrent")) {
		t.Fatalf("сохранённый .torrent не совпадает: %v", err)
	}
}

func TestSourceRutorReportsRemoved(t *testing.T) {
	s := rutortest.NewServer(t)
	code, _, errOut := runSource(t, "details", "--mirror", s.Mirror.URL, "99999999")
	if code != 1 || !strings.Contains(errOut, "удалена") {
		t.Fatalf("код %d: %s", code, errOut)
	}
}

func TestSourceUsage(t *testing.T) {
	for _, args := range [][]string{{"source"}, {"source", "rutracker", "top", "1"}, {"source", "rutor", "top"}, {"source", "rutor", "dance"}} {
		var out, errb bytes.Buffer
		if code := runCLI(args, &out, &errb); code != 2 || !strings.Contains(errb.String(), "Использование") {
			t.Errorf("%v: код %d, %q", args, code, errb.String())
		}
	}
}
