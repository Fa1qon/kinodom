package main

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"kinodom/internal/source/rutor/rutortest"
	"kinodom/internal/source/rutracker/rutrackertest"
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
	for _, args := range [][]string{{"source"}, {"source", "kinozal", "top", "1"}, {"source", "rutor", "top"}, {"source", "rutor", "dance"}} {
		var out, errb bytes.Buffer
		if code := runCLI(args, &out, &errb); code != 2 || !strings.Contains(errb.String(), "Использование") {
			t.Errorf("%v: код %d, %q", args, code, errb.String())
		}
	}
}

// Поиск прошёл не везде — найденное показывается, а в поток ошибок идёт предупреждение.
func TestSourceRutorSearchPartial(t *testing.T) {
	s := rutortest.NewServer(t)
	s.Override = func(w http.ResponseWriter, r *http.Request) bool {
		if strings.HasPrefix(r.URL.Path, "/search/0/12/") {
			w.WriteHeader(http.StatusBadGateway)
			return true
		}
		return false
	}
	code, out, errOut := runSource(t, "search", "--mirror", s.Mirror.URL, "Матрица")
	if code != 0 || !strings.Contains(out, "Найдено") || !strings.Contains(errOut, "поиск прошёл не везде") {
		t.Fatalf("код %d\n%s\n%s", code, out, errOut)
	}
}

func runRutracker(t *testing.T, s *rutrackertest.Server, args ...string) (int, string, string) {
	t.Helper()
	base := []string{"source", "rutracker", args[0], "--mirror", s.Forum.URL, "--api", s.API.URL, "--feed", s.Feed.URL, "--no-edge"}
	var out, errb bytes.Buffer
	code := runCLI(append(base, args[1:]...), &out, &errb)
	return code, out.String(), errb.String()
}

// noRutrackerEnv — пара Rutracker разработчика из окружения не должна попадать в тесты.
func noRutrackerEnv(t *testing.T) {
	t.Setenv("KINODOM_RUTRACKER_LOGIN", "")
	t.Setenv("KINODOM_RUTRACKER_PASSWORD", "")
}

func TestSourceRutrackerAPI(t *testing.T) {
	noRutrackerEnv(t)
	s := rutrackertest.NewServer(t)
	code, out, errOut := runRutracker(t, s, "categories")
	if code != 0 || !strings.Contains(out, "[Док] Космос") {
		t.Fatalf("categories: код %d\n%s\n%s", code, out, errOut)
	}
	code, out, errOut = runRutracker(t, s, "top", "--limit", "3", "2076")
	if code != 0 || !strings.Contains(out, "Найдено 100 раздач") {
		t.Fatalf("top: код %d\n%s\n%s", code, out, errOut)
	}
	code, out, errOut = runRutracker(t, s, "recent", "--limit", "2", "313")
	if code != 0 || !strings.Contains(out, "Иностранец / The Foreigner") {
		t.Fatalf("recent: код %d\n%s\n%s", code, out, errOut)
	}
}

func TestSourceRutrackerDetailsAndSearch(t *testing.T) {
	noRutrackerEnv(t)
	s := rutrackertest.NewServer(t)
	s.Login, s.Password = "user", "pass"
	code, out, errOut := runRutracker(t, s, "details", "6914565")
	if code != 0 || !strings.Contains(out, "Экстрасенсы") {
		t.Fatalf("details: код %d\n%s\n%s", code, out, errOut)
	}
	if n := s.Logins(); n != 0 {
		t.Fatalf("details без пары в окружении теста вошёл %d раз — пара взята из окружения разработчика", n)
	}
	t.Setenv("KINODOM_RUTRACKER_LOGIN", "user")
	t.Setenv("KINODOM_RUTRACKER_PASSWORD", "pass")
	code, out, errOut = runRutracker(t, s, "search", "--limit", "3", "космос")
	if code != 0 || !strings.Contains(out, "Космос") {
		t.Fatalf("search: код %d\n%s\n%s", code, out, errOut)
	}
}

func TestSourceRutrackerSearchNeedsCredentials(t *testing.T) {
	s := rutrackertest.NewServer(t)
	t.Setenv("KINODOM_RUTRACKER_LOGIN", "")
	code, _, errOut := runRutracker(t, s, "search", "космос")
	if code != 1 || !strings.Contains(errOut, "KINODOM_RUTRACKER_LOGIN") {
		t.Fatalf("код %d: %s", code, errOut)
	}
}

// search-raw: несколько наборов параметров за один вход, у каждого — сводка по разделам.
func TestSourceRutrackerSearchRaw(t *testing.T) {
	s := rutrackertest.NewServer(t)
	s.Login, s.Password = "user", "pass"
	t.Setenv("KINODOM_RUTRACKER_LOGIN", "user")
	t.Setenv("KINODOM_RUTRACKER_PASSWORD", "pass")
	code, out, errOut := runRutracker(t, s, "search-raw", "--limit", "2", "nm=космос", "f=2076&nm=космос")
	if code != 0 || strings.Count(out, "Разделы: 2076×") != 2 || s.LastForums() != "2076" || s.Logins() != 1 {
		t.Fatalf("код %d, входов %d\n%s\n%s", code, s.Logins(), out, errOut)
	}
}
