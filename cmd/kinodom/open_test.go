package main

import (
	"bytes"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"kinodom/internal/config"
	"kinodom/internal/player"
)

// kinodom open: ссылка разбирается строго, плеер — из настроек работающего сервера, запускается с
// потоком и названием отдельными аргументами.
func TestOpenLaunchesChosenPlayer(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/settings" {
			fmt.Fprint(w, `{"player":"mpc-hc"}`)
		}
	}))
	t.Cleanup(api.Close)
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(api.URL, "http://"))
	home := t.TempDir()
	t.Setenv(config.EnvHome, home)
	os.WriteFile(filepath.Join(home, "kinodom.json"), []byte(`{"apiPort":`+port+`,"torrentPort":42000}`), 0o644)
	var asked string
	var launched []string
	findPlayer = func(choice string) (player.Player, error) {
		asked = choice
		return player.Player{Name: "MPC-HC", Path: `C:\MPC\mpc-hc64.exe`}, nil
	}
	launchPlayer = func(p player.Player, stream, title string, start int) error {
		launched = []string{p.Path, stream, title, strconv.Itoa(start)}
		return nil
	}
	t.Cleanup(func() { findPlayer, launchPlayer = player.Find, player.Launch })
	n, _ := strconv.Atoi(port)
	stream := fmt.Sprintf("http://127.0.0.1:%d/stream/ab/0/film.mkv", n)
	var out, errb bytes.Buffer
	if code := runCLI([]string{"open", player.LaunchURL(stream, "Фильм")}, &out, &errb); code != 0 {
		t.Fatalf("код %d: %s", code, errb.String())
	}
	if asked != "mpc-hc" || len(launched) != 4 || launched[1] != stream || launched[2] != "Фильм" || launched[3] != "0" {
		t.Fatalf("плеер %q, запуск %v", asked, launched)
	}
	// Продолжить с места (спека этапа 8, раздел 7.3): место из ссылки доходит до плеера.
	if code := runCLI([]string{"open", player.LaunchURLAt(stream, "Фильм", 1790)}, &out, &errb); code != 0 || launched[3] != "1790" {
		t.Fatalf("с места: код %d, запуск %v", code, launched)
	}
	launched = nil
	if code := runCLI([]string{"open", "kinodom://play?url=http://127.0.0.1:" + port + "/api/v1/settings"}, &out, &errb); code != 1 ||
		launched != nil || !strings.Contains(errb.String(), "не от Kinodom") {
		t.Fatalf("чужая ссылка: код %d, запуск %v, %s", code, launched, errb.String())
	}
}

func TestProtocolNeedsAction(t *testing.T) {
	var out, errb bytes.Buffer
	if code := runCLI([]string{"protocol"}, &out, &errb); code != 2 || !strings.Contains(errb.String(), "install") {
		t.Fatalf("код %d: %s", code, errb.String())
	}
}
