//go:build windows

package main

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"kinodom/internal/config"
	"kinodom/internal/player"
	"kinodom/internal/winsvc"
)

// asGUI — как kinodomw.exe: окна вместо консоли (подделка), журнал — во временной папке.
func asGUI(t *testing.T) (*[]string, string) {
	t.Helper()
	var shown []string
	savedGUI, savedBox := gui, messageBox
	gui, messageBox = "1", func(text string) { shown = append(shown, text) }
	t.Cleanup(func() { gui, messageBox = savedGUI, savedBox })
	local := t.TempDir()
	t.Setenv("LOCALAPPDATA", local)
	return &shown, filepath.Join(local, "Kinodom", "open.log")
}

// fakeKinodom — служба на этом ПК: kinodom.json с её портом; access отвечает kind для путей.
func fakeKinodom(t *testing.T, kinds map[string]string) (int, *[]string) {
	t.Helper()
	var mu sync.Mutex
	var calls []string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.Path)
		mu.Unlock()
		switch r.URL.Path {
		case "/api/v1/library/access":
			fmt.Fprintf(w, `{"kind":%q,"readable":false}`, kinds[r.URL.Query().Get("path")])
		case "/api/v1/library/scan":
			if r.Header.Get("Content-Type") != "application/json" {
				w.WriteHeader(http.StatusUnsupportedMediaType)
			}
		}
	}))
	t.Cleanup(api.Close)
	_, p, _ := net.SplitHostPort(strings.TrimPrefix(api.URL, "http://"))
	port, _ := strconv.Atoi(p)
	home := t.TempDir()
	t.Setenv(config.EnvHome, home)
	config.SaveBootstrap(filepath.Join(home, "kinodom.json"), config.Bootstrap{APIPort: port, TorrentPort: 42000})
	return port, &calls
}

// Ошибка ссылки в kinodomw.exe — окно Windows с коротким текстом, подробности — в журнал
// пользователя (хвост 7a: консоли нет, ошибку иначе никто не увидит).
func TestOpenErrorShowsWindowAndLogs(t *testing.T) {
	shown, logPath := asGUI(t)
	port, _ := fakeKinodom(t, nil)
	findPlayer = func(string) (player.Player, error) {
		return player.Player{}, errors.New("не найден ни VLC, ни MPC-HC")
	}
	t.Cleanup(func() { findPlayer = player.Find })
	stream := fmt.Sprintf("http://127.0.0.1:%d/stream/ab/0/film.mkv", port)
	if code, _, _ := runCmd(cmdOpen, player.LaunchURL(stream, "Фильм")); code != 1 {
		t.Fatalf("код %d", code)
	}
	if code, _, _ := runCmd(cmdOpen, "kinodom://play?url=http://evil.example/x"); code != 1 {
		t.Fatalf("чужая ссылка: код %d", code)
	}
	if len(*shown) != 2 || !strings.Contains((*shown)[0], "VLC") || !strings.Contains((*shown)[1], "не от Kinodom") {
		t.Fatalf("окна %q", *shown)
	}
	b, err := os.ReadFile(logPath)
	if err != nil || !strings.Contains(string(b), "не найден ни VLC") || !strings.Contains(string(b), "evil.example") {
		t.Fatalf("журнал %q, %v", b, err)
	}
	// Журнал не растёт без предела: больше 1 МБ — начинается заново.
	os.WriteFile(logPath, make([]byte, 1<<20+1), 0o644)
	runCmd(cmdOpen, "kinodom://play?url=x")
	if fi, _ := os.Stat(logPath); fi.Size() > 1<<20 {
		t.Fatalf("журнал %d байт", fi.Size())
	}
}

// «Разрешить доступ»: служба подтверждает папку → перезапуск себя от администратора командой grant
// (--write: в папки загрузок и медиатеки Kinodom качает и из них удаляет, план 14В) → служба обходит медиатеку. Чужая папка — окно отказа, прав никто не просит.
func TestOpenGrant(t *testing.T) {
	shown, _ := asGUI(t)
	movies, downloads, foreign := `D:\Share\Movies`, `E:\Kinodom`, `C:\Windows`
	port, calls := fakeKinodom(t, map[string]string{movies: "library", downloads: "downloads"})
	var elevated [][]string
	savedElevate := elevate
	elevate = func(exe string, args []string) error { elevated = append(elevated, args); return nil }
	t.Cleanup(func() { elevate = savedElevate })

	if code, _, errOut := runCmd(cmdOpen, player.GrantURL(movies, port)); code != 0 {
		t.Fatalf("медиатека: код %d %s", code, errOut)
	}
	if code, _, _ := runCmd(cmdOpen, player.GrantURL(downloads, port)); code != 0 {
		t.Fatal("загрузки")
	}
	want := [][]string{{"grant", "--write", movies}, {"grant", "--write", downloads}}
	if !slices.EqualFunc(elevated, want, slices.Equal) {
		t.Fatalf("повышение прав %q", elevated)
	}
	if n := strings.Count(strings.Join(*calls, ";"), "POST /api/v1/library/scan"); n != 2 {
		t.Fatalf("обходов %d: %v", n, *calls)
	}
	if code, _, _ := runCmd(cmdOpen, player.GrantURL(foreign, port)); code != 1 || len(elevated) != 2 {
		t.Fatalf("чужая папка: код %d, повышений %d", code, len(elevated))
	}
	if len(*shown) != 1 || !strings.Contains((*shown)[0], "не из настроек Kinodom") {
		t.Fatalf("окна %q", *shown)
	}
	// Человек нажал «Нет» в окне Windows — без окна ошибки.
	elevate = func(string, []string) error { return winsvc.ErrCancelled }
	if code, _, _ := runCmd(cmdOpen, player.GrantURL(movies, port)); code != 1 || len(*shown) != 1 {
		t.Fatalf("отказ в окне прав: код %d, окна %q", code, *shown)
	}
	// Повышенный grant отказал и сам показал окно (хвост Х41) — второго окна нет, только журнал.
	elevate = func(string, []string) error { return fmt.Errorf("grant: %w", winsvc.ErrElevatedFailed) }
	if code, _, _ := runCmd(cmdOpen, player.GrantURL(movies, port)); code != 1 || len(*shown) != 1 {
		t.Fatalf("отказ повышенного grant: код %d, окна %q", code, *shown)
	}
	// Повышение не запустилось вовсе — окно одно, от этого процесса.
	elevate = func(string, []string) error {
		return errors.New("запуск от администратора: нет процесса")
	}
	if code, _, _ := runCmd(cmdOpen, player.GrantURL(movies, port)); code != 1 || len(*shown) != 2 {
		t.Fatalf("повышение не запустилось: код %d, окна %q", code, *shown)
	}
}

// kinodom grant — только от администратора, только папка на диске этого ПК; права — учётной
// записи службы, изменение — с --write.
func TestGrantCommand(t *testing.T) {
	f, _ := withFake(t)
	dir := t.TempDir()
	if code, _, errOut := runCmd(cmdGrant, "--write", dir); code != 0 {
		t.Fatalf("код %d: %s", code, errOut)
	}
	if got := f.Actions(); len(got) != 1 || got[0] != "acl.grant "+dir+" "+winsvc.ServiceAccount+" write" {
		t.Fatalf("действия %v", got)
	}
	// Корень диска — отказ: наследуемые права ушли бы на весь диск (ревью, мелочь 15).
	for _, bad := range []string{`\\server\share`, "Movies", filepath.Join(dir, "нет такой"), `C:\`, "C:/"} {
		if code, _, _ := runCmd(cmdGrant, bad); code == 0 {
			t.Errorf("%s: принято", bad)
		}
	}
	f.Admin = false
	if code, _, errOut := runCmd(cmdGrant, dir); code != 1 || !strings.Contains(errOut, "администратора") {
		t.Fatalf("без прав администратора: %d %s", code, errOut)
	}
}
