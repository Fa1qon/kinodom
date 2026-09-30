package main

import (
	"errors"
	"strings"
	"testing"

	"kinodom/internal/tray"
	"kinodom/internal/winsvc"
)

// fakeTray — значок в трее без Windows: что вызывалось.
type fakeTray struct {
	state    string
	startErr error
	haltErr  error
	first    bool
	calls    []string
	items    []tray.Item
	messages []string
}

func (f *fakeTray) deps() trayDeps {
	return trayDeps{
		state:   func() (string, error) { f.calls = append(f.calls, "state"); return f.state, nil },
		start:   func() error { f.calls = append(f.calls, "start"); return f.startErr },
		halt:    func() error { f.calls = append(f.calls, "halt"); return f.haltErr },
		ready:   func() { f.calls = append(f.calls, "ready") },
		openURL: func() { f.calls = append(f.calls, "open") },
		single: func() (func(), bool) {
			f.calls = append(f.calls, "single")
			return func() {}, f.first
		},
		run: func(items []tray.Item, def func()) error {
			f.calls = append(f.calls, "run")
			f.items = items
			return nil
		},
		quit:    func() { f.calls = append(f.calls, "quit") },
		message: func(s string) { f.messages = append(f.messages, s) },
	}
}

// Значок в трее (спека этапа 11a, раздел 5.2): служба остановлена — запускается; --open — пульт в
// браузере, когда сервер ответит; меню — «Открыть Kinodom» и «Выход» (остановить сервер, убрать значок).
func TestTrayStartsServerAndMenu(t *testing.T) {
	f := &fakeTray{state: winsvc.StateStopped, first: true}
	if code := trayMain(f.deps(), true); code != 0 {
		t.Fatalf("код %d, окна %v", code, f.messages)
	}
	if got := strings.Join(f.calls, ","); got != "state,start,ready,open,single,run" {
		t.Fatalf("вызовы %s", got)
	}
	if len(f.items) != 2 || f.items[0].Title != "Открыть Kinodom" || f.items[1].Title != "Выход" {
		t.Fatalf("меню %+v", f.items)
	}
	f.calls = nil
	f.items[1].Do()
	if got := strings.Join(f.calls, ","); got != "halt,quit" {
		t.Fatalf("«Выход»: %s", got)
	}
	f.calls = nil
	f.items[0].Do()
	if got := strings.Join(f.calls, ","); got != "open" {
		t.Fatalf("«Открыть»: %s", got)
	}
}

// Автозапуск при входе: служба уже работает — не трогается, пульт не открывается.
func TestTrayAutostart(t *testing.T) {
	f := &fakeTray{state: winsvc.StateRunning, first: true}
	trayMain(f.deps(), false)
	if got := strings.Join(f.calls, ","); got != "state,single,run" {
		t.Fatalf("вызовы %s", got)
	}
}

// Ярлык «Kinodom», когда значок уже есть: второго значка нет — только сервер и пульт.
func TestTraySecondInstance(t *testing.T) {
	f := &fakeTray{state: winsvc.StateRunning, first: false}
	if code := trayMain(f.deps(), true); code != 0 {
		t.Fatalf("код %d", code)
	}
	if got := strings.Join(f.calls, ","); got != "state,ready,open,single" {
		t.Fatalf("вызовы %s", got)
	}
}

// Отказы — окном: Kinodom не установлен; сервер не запускается; «Выход» не остановил сервер —
// значок остаётся.
func TestTrayFailures(t *testing.T) {
	f := &fakeTray{state: winsvc.StateNotFound, first: true}
	if code := trayMain(f.deps(), true); code != 1 || len(f.messages) != 1 || !strings.Contains(f.messages[0], "не установлен") {
		t.Fatalf("не установлен: код %d, окна %v", code, f.messages)
	}
	f = &fakeTray{state: winsvc.StateStopped, startErr: errors.New("отказано в доступе"), first: true}
	if code := trayMain(f.deps(), true); code != 1 || len(f.messages) != 1 || !strings.Contains(f.messages[0], "не запускается") {
		t.Fatalf("не запускается: код %d, окна %v", code, f.messages)
	}
	f = &fakeTray{state: winsvc.StateRunning, haltErr: errors.New("отказано в доступе"), first: true}
	trayMain(f.deps(), false)
	f.calls = nil
	f.items[1].Do()
	if got := strings.Join(f.calls, ","); got != "halt" || len(f.messages) != 1 || !strings.Contains(f.messages[0], "не остановился") {
		t.Fatalf("«Выход» не удался: %s, окна %v", got, f.messages)
	}
}
