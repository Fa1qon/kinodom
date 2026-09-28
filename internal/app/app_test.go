package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kinodom/internal/api"
	"kinodom/internal/config"
	"kinodom/internal/store"
	"kinodom/internal/supervisor"
)

// startApp поднимает сервер целиком во временной папке на случайном порту.
func startApp(t *testing.T) *App {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	a, err := New(ctx, Options{Home: t.TempDir(), ListenAddr: "127.0.0.1:0"})
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { a.Run(ctx); close(done) }()
	t.Cleanup(func() {
		cancel()
		<-done
		a.Close()
	})
	select {
	case <-a.API.Ready():
	case <-time.After(5 * time.Second):
		t.Fatal("API не поднялся за 5 с")
	}
	return a
}

func getJSON(t *testing.T, url string, v any) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: код %d", url, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatal(err)
	}
}

// waitAllRunning ждёт, пока все включённые модули перейдут в running.
func waitAllRunning(t *testing.T, a *App) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		ok := true
		for _, st := range a.Sup.Status() {
			if st.State != supervisor.StateRunning && st.State != supervisor.StateDisabled {
				ok = false
			}
		}
		if ok {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("не все модули работают: %+v", a.Sup.Status())
}

func TestAllModulesTogether(t *testing.T) {
	a := startApp(t)
	waitAllRunning(t, a)
	var st struct {
		Problems []any               `json:"problems"`
		Modules  []supervisor.Status `json:"modules"`
	}
	getJSON(t, "http://"+a.API.Addr()+"/api/v1/status", &st)
	names := map[string]supervisor.State{}
	for _, m := range st.Modules {
		names[m.Name] = m.State
	}
	if names["api"] != supervisor.StateRunning {
		t.Fatalf("модули в /status: %+v", st.Modules)
	}
	if st.Problems == nil {
		t.Fatal("problems должен быть [], а не null")
	}
}

func TestModuleCanBeDisabledBySetting(t *testing.T) {
	ctx := context.Background()
	a, err := New(ctx, Options{Home: t.TempDir(), ListenAddr: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if !a.ModuleEnabled(ctx, "iptv") {
		t.Fatal("по умолчанию модуль включён")
	}
	if err := a.DB.SetSetting(ctx, "modules.iptv.enabled", "false"); err != nil {
		t.Fatal(err)
	}
	if a.ModuleEnabled(ctx, "iptv") {
		t.Fatal("настройка modules.iptv.enabled=false не выключила модуль")
	}
}

// Порт может занять чужая программа по-разному: только IPv4, только 127.0.0.1, только ::1
// или двухстеково. В любом случае сервер не должен стартовать «наполовину».
func TestBusyPortIsDetectedInEveryForm(t *testing.T) {
	forms := []struct{ network, addr string }{
		{"tcp4", "0.0.0.0:0"},
		{"tcp4", "127.0.0.1:0"},
		{"tcp6", "[::1]:0"},
		{"tcp", ":0"},
	}
	for _, f := range forms {
		ln, err := net.Listen(f.network, f.addr)
		if err != nil {
			t.Logf("%s %s: не удалось занять порт (%v), пропускаю", f.network, f.addr, err)
			continue
		}
		port := ln.Addr().(*net.TCPAddr).Port
		a, err := New(context.Background(), Options{Home: t.TempDir(), ListenAddr: fmt.Sprintf(":%d", port)})
		ln.Close()
		if err == nil {
			a.Close()
			t.Errorf("%s %s: порт %d занят, а сервер запустился", f.network, f.addr, port)
			continue
		}
		if !errors.Is(err, api.ErrPortBusy) || !strings.Contains(err.Error(), "занят") {
			t.Errorf("%s %s: ожидалась ошибка «порт занят», получено %v", f.network, f.addr, err)
		}
	}
}

// Служба работает без консоли: причина отказа запуска должна попасть в журнал.
func TestStartupFailureIsWrittenToLog(t *testing.T) {
	ctx := context.Background()
	cases := map[string]func(t *testing.T, home string){
		"новее": func(t *testing.T, home string) { // база от более новой версии Kinodom
			db, err := store.Open(ctx, config.NewPaths(home).DB)
			if err != nil {
				t.Fatal(err)
			}
			db.W.Exec("PRAGMA user_version = 999")
			db.Close()
		},
		"kinodom.json": func(t *testing.T, home string) {
			os.WriteFile(filepath.Join(home, "kinodom.json"), []byte("{"), 0o644)
		},
	}
	for want, prepare := range cases {
		home := t.TempDir()
		prepare(t, home)
		if a, err := New(ctx, Options{Home: home, ListenAddr: "127.0.0.1:0"}); err == nil {
			a.Close()
			t.Fatalf("%s: запуск должен был отказать", want)
		}
		data, err := os.ReadFile(filepath.Join(config.NewPaths(home).Logs, "kinodom.log"))
		if err != nil || !strings.Contains(string(data), "Kinodom не запустился") || !strings.Contains(string(data), want) {
			t.Fatalf("%s: в журнале нет причины отказа (%v): %s", want, err, data)
		}
	}
}
