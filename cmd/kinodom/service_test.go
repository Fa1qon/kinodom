package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/windows/svc"

	"kinodom/internal/config"
	"kinodom/internal/setup"
	"kinodom/internal/winsvc"
	"kinodom/internal/winsvc/winsvctest"
)

// fakeServer — приложение под службой: работает до отмены или до своей ошибки.
type fakeServer struct {
	quits  bool // Run завершается сам, без отмены (приложение упало)
	closed atomic.Bool
}

func (f *fakeServer) Run(ctx context.Context) {
	if !f.quits {
		<-ctx.Done()
	}
}

func (f *fakeServer) Close() error { f.closed.Store(true); return nil }

// execute запускает обработчик службы и возвращает каналы, как диспетчер служб.
func execute(t *testing.T, h *serviceHandler) (chan svc.ChangeRequest, chan svc.Status, func() (bool, uint32)) {
	t.Helper()
	r, s := make(chan svc.ChangeRequest), make(chan svc.Status, 10)
	type result struct {
		ssec bool
		code uint32
	}
	done := make(chan result, 1)
	go func() {
		ssec, code := h.Execute(nil, r, s)
		done <- result{ssec, code}
	}()
	return r, s, func() (bool, uint32) {
		select {
		case res := <-done:
			return res.ssec, res.code
		case <-time.After(5 * time.Second):
			t.Fatal("обработчик службы не завершился")
			return false, 0
		}
	}
}

func waitState(t *testing.T, s chan svc.Status, want svc.State) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case st := <-s:
			if st.State == want {
				return
			}
		case <-deadline:
			t.Fatalf("служба не дошла до состояния %d", want)
		}
	}
}

// «Остановить» в диспетчере служб — штатное закрытие приложения, как Ctrl+C у run.
func TestServiceStopsAppOnStop(t *testing.T) {
	app := &fakeServer{}
	h := &serviceHandler{start: func(context.Context) (server, error) { return app, nil }}
	r, s, wait := execute(t, h)
	waitState(t, s, svc.Running)
	r <- svc.ChangeRequest{Cmd: svc.Stop}
	waitState(t, s, svc.StopPending)
	if ssec, code := wait(); ssec || code != 0 || !app.closed.Load() {
		t.Fatalf("выход %v %d, закрыто %v", ssec, code, app.closed.Load())
	}
}

// Приложение не запустилось или упало само — выход с ошибкой: Windows перезапустит службу через
// 5 с (восстановление и при выходе с ошибкой без падения).
func TestServiceFailsWithErrorCode(t *testing.T) {
	h := &serviceHandler{start: func(context.Context) (server, error) { return nil, errors.New("порт занят") }}
	_, _, wait := execute(t, h)
	if ssec, code := wait(); !ssec || code == 0 {
		t.Fatalf("отказ запуска: %v %d", ssec, code)
	}
	app := &fakeServer{quits: true}
	h = &serviceHandler{start: func(context.Context) (server, error) { return app, nil }}
	_, _, wait = execute(t, h)
	if ssec, code := wait(); !ssec || code == 0 || !app.closed.Load() {
		t.Fatalf("приложение упало: %v %d, закрыто %v", ssec, code, app.closed.Load())
	}
}

// withFake подменяет систему этого ПК подделкой, папку данных — временной.
func withFake(t *testing.T) (*winsvctest.Fake, string) {
	t.Helper()
	f := winsvctest.New()
	saved := newSystem
	newSystem = f.System
	t.Cleanup(func() { newSystem = saved })
	home := t.TempDir()
	t.Setenv(config.EnvHome, home)
	return f, home
}

func runCmd(fn func([]string, io.Writer, io.Writer) int, args ...string) (int, string, string) {
	var out, errOut bytes.Buffer
	code := fn(args, &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestInstallCommand(t *testing.T) {
	f, home := withFake(t)
	dl := filepath.Join(t.TempDir(), "Kinodom")
	code, out, errOut := runCmd(cmdInstall, "--downloads", dl, "--no-start")
	if code != 0 || !strings.Contains(out, "Kinodom установлен") {
		t.Fatalf("код %d\n%s\n%s", code, out, errOut)
	}
	c, ok := f.Services[setup.ServiceName]
	exe, _ := os.Executable()
	if !ok || c.Exe != filepath.Join(filepath.Dir(exe), "kinodom.exe") {
		t.Fatalf("служба %+v", c)
	}
	if _, err := os.Stat(filepath.Join(home, "kinodom.json")); err != nil {
		t.Fatalf("kinodom.json: %v", err)
	}
	f.Admin = false
	if code, _, errOut := runCmd(cmdInstall); code != 1 || !strings.Contains(errOut, "администратора") {
		t.Fatalf("без прав администратора: %d %s", code, errOut)
	}
	if code, _, _ := runCmd(cmdInstall, "лишнее"); code != 2 {
		t.Fatalf("лишний аргумент: %d", code)
	}
}

func TestUninstallCommand(t *testing.T) {
	f, home := withFake(t)
	if code, out, errOut := runCmd(cmdInstall, "--downloads", filepath.Join(t.TempDir(), "K"), "--no-start"); code != 0 {
		t.Fatalf("установка: %d %s %s", code, out, errOut)
	}
	if code, _, errOut := runCmd(cmdUninstall, "--purge"); code != 0 {
		t.Fatalf("удаление: %d %s", code, errOut)
	}
	if _, ok := f.Services[setup.ServiceName]; ok {
		t.Fatal("служба осталась")
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("данные остались: %v", err)
	}
}

// Службы нет — stop ничего не делает (установщик вызывает её перед заменой файлов).
func TestStopWithoutService(t *testing.T) {
	withFake(t)
	if code, _, errOut := runCmd(cmdStop); code != 0 {
		t.Fatalf("код %d: %s", code, errOut)
	}
}

// Служба не остановилась за 60 с (Review Focus 1): понятный отказ — установщик откатит обновление,
// старая версия цела.
func TestStopWaitsAndFails(t *testing.T) {
	f, _ := withFake(t)
	f.Services[setup.ServiceName] = winsvc.ServiceConfig{Name: setup.ServiceName}
	f.Running[setup.ServiceName], f.Stuck = true, true
	code, _, errOut := runCmd(cmdStop)
	if code != 1 || !strings.Contains(errOut, "Kinodom") || !strings.Contains(errOut, "не остановилась") {
		t.Fatalf("код %d: %s", code, errOut)
	}
}

// check: служба, пульт (версия и проблемы), правила брандмауэра, ссылка kinodom://.
func TestCheckCommand(t *testing.T) {
	f, home := withFake(t)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"version": "0.11.0-test", "problems": []map[string]string{{"id": "proxy.down", "text": "Прокси не отвечает"}}})
	}))
	t.Cleanup(api.Close)
	u, _ := url.Parse(api.URL)
	port, _ := strconv.Atoi(u.Port())
	config.SaveBootstrap(filepath.Join(home, "kinodom.json"), config.Bootstrap{APIPort: port, TorrentPort: 42000})
	exe, _ := os.Executable()
	f.Services[setup.ServiceName] = winsvc.ServiceConfig{Name: setup.ServiceName}
	f.Running[setup.ServiceName] = true
	f.Rules[setup.RuleAPI], f.Rules[setup.RuleTorrents] = winsvc.FirewallRule{}, winsvc.FirewallRule{}
	f.Protocols[setup.Scheme] = setup.OpenCommand(filepath.Dir(exe))
	code, out, errOut := runCmd(cmdCheck)
	if code != 0 || !strings.Contains(out, "работает") || !strings.Contains(out, "0.11.0-test") || !strings.Contains(out, "Прокси не отвечает") {
		t.Fatalf("код %d\n%s\n%s", code, out, errOut)
	}
	f.Protocols[setup.Scheme] = `"C:\old\kinodom.exe" open "%1"`
	delete(f.Rules, setup.RuleTorrents)
	code, out, _ = runCmd(cmdCheck)
	if code != 1 || !strings.Contains(out, setup.RuleTorrents) || !strings.Contains(out, "kinodom://") {
		t.Fatalf("неполадки: код %d\n%s", code, out)
	}
	delete(f.Running, setup.ServiceName)
	api.Close()
	if code, out, _ = runCmd(cmdCheck); code != 1 || !strings.Contains(out, "не отвечает") {
		t.Fatalf("служба остановлена: код %d\n%s", code, out)
	}
}
