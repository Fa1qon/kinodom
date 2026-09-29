package power

import (
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

type fakeRequester struct {
	mu           sync.Mutex
	sets, clrs   int
	held, closed bool
}

func (f *fakeRequester) set() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sets++
	f.held = true
	return nil
}

func (f *fakeRequester) clear() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.clrs++
	f.held = false
	return nil
}

func (f *fakeRequester) close() error { f.closed = true; return nil }

func (f *fakeRequester) state() (sets, clrs int, held bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sets, f.clrs, f.held
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func waitHeld(t *testing.T, f *fakeRequester, want bool) {
	t.Helper()
	for deadline := time.Now().Add(2 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		if _, _, held := f.state(); held == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("запрет сна держится = %v, ожидалось %v", !want, want)
		}
	}
}

// Два потока — один запрос; запрет держится, пока открыт хоть один, и ещё немного после.
func TestKeeperHoldsWhileStreamsAndAfter(t *testing.T) {
	f := &fakeRequester{}
	k := newKeeper(f, 100*time.Millisecond, quiet())
	a := k.Acquire()
	b := k.Acquire()
	if sets, _, held := f.state(); sets != 1 || !held || k.Active() != 2 {
		t.Fatalf("запросов %d, держится %v, потоков %d", sets, held, k.Active())
	}
	a()
	a() // повторное «закончился» безвредно
	if k.Active() != 1 {
		t.Fatalf("потоков %d", k.Active())
	}
	b()
	if _, _, held := f.state(); !held {
		t.Fatal("запрет снят сразу после последнего потока — нужно подождать")
	}
	waitHeld(t, f, false)
	if _, clrs, _ := f.state(); clrs != 1 {
		t.Fatalf("снятий %d", clrs)
	}
}

// Новый поток в течение выдержки — запрет не снимается и не запрашивается заново; устаревший
// таймер не снимает запрет раньше времени.
func TestKeeperNewStreamDuringHold(t *testing.T) {
	f := &fakeRequester{}
	k := newKeeper(f, 300*time.Millisecond, quiet())
	k.Acquire()()
	time.Sleep(200 * time.Millisecond)
	release := k.Acquire()
	release()
	time.Sleep(200 * time.Millisecond) // первый таймер уже сработал бы — запрет держится
	if sets, clrs, held := f.state(); sets != 1 || clrs != 0 || !held {
		t.Fatalf("запросов %d, снятий %d, держится %v", sets, clrs, held)
	}
	waitHeld(t, f, false)
	k.Close()
	if !f.closed {
		t.Fatal("запрос не закрыт")
	}
}

func TestNilKeeperDoesNothing(t *testing.T) {
	var k *Keeper
	k.Acquire()()
	if k.Active() != 0 || k.Close() != nil {
		t.Fatal("nil-хранитель что-то делает")
	}
}
