package supervisor

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeModule struct {
	name string
	run  func(ctx context.Context) error
}

func (f fakeModule) Name() string                  { return f.name }
func (f fakeModule) Run(ctx context.Context) error { return f.run(ctx) }

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("не дождались: %s", what)
}

func statusOf(s *Supervisor, name string) Status {
	for _, st := range s.Status() {
		if st.Name == name {
			return st
		}
	}
	return Status{}
}

func TestPanicRestartsOnlyThatModule(t *testing.T) {
	var panics, calmExits atomic.Int32
	s := New(quiet(), WithBackoff(10*time.Millisecond))
	s.Add(fakeModule{"bad", func(ctx context.Context) error { panics.Add(1); panic("бум") }}, true)
	s.Add(fakeModule{"calm", func(ctx context.Context) error { <-ctx.Done(); calmExits.Add(1); return nil }}, true)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()

	waitFor(t, "три запуска упавшего модуля", func() bool { return panics.Load() >= 3 })
	if st := statusOf(s, "calm"); st.State != StateRunning || calmExits.Load() != 0 {
		t.Fatalf("соседний модуль задело: %+v, выходов %d", st, calmExits.Load())
	}
	bad := statusOf(s, "bad")
	if bad.Restarts < 2 || !strings.Contains(bad.LastError, "паника: бум") || bad.LastErrorAt.IsZero() {
		t.Fatalf("состояние упавшего модуля: %+v", bad)
	}
	if strings.Contains(bad.LastError, "\n") {
		t.Fatalf("стек не должен попадать в текст для людей: %q", bad.LastError)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run не вернулся после отмены")
	}
	if st := statusOf(s, "calm"); st.State != StateStopped {
		t.Fatalf("после отмены: %+v", st)
	}
}

func TestErrorAndEarlyReturnAreFailures(t *testing.T) {
	var mu sync.Mutex
	var sink []string
	s := New(quiet(), WithBackoff(10*time.Millisecond), WithErrorSink(func(m, text string) {
		mu.Lock()
		sink = append(sink, m+": "+text)
		mu.Unlock()
	}))
	s.Add(fakeModule{"err", func(ctx context.Context) error { return errors.New("нет сети") }}, true)
	s.Add(fakeModule{"early", func(ctx context.Context) error { return nil }}, true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)
	waitFor(t, "обе ошибки в журнале", func() bool {
		mu.Lock()
		defer mu.Unlock()
		joined := strings.Join(sink, "|")
		return strings.Contains(joined, "err: нет сети") &&
			strings.Contains(joined, "early: модуль завершился, хотя его не останавливали")
	})
}

func TestDisabledModuleIsNotStarted(t *testing.T) {
	var started atomic.Bool
	s := New(quiet())
	s.Add(fakeModule{"off", func(ctx context.Context) error { started.Store(true); <-ctx.Done(); return nil }}, false)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	s.Run(ctx)
	if started.Load() {
		t.Fatal("выключенный модуль запустился")
	}
	if st := statusOf(s, "off"); st.State != StateDisabled || s.IsRunning("off") {
		t.Fatalf("состояние: %+v", st)
	}
}

func TestBackoffGrows(t *testing.T) {
	var runs atomic.Int32
	s := New(quiet(), WithBackoff(10*time.Millisecond, 300*time.Millisecond))
	s.Add(fakeModule{"flaky", func(ctx context.Context) error { runs.Add(1); return errors.New("сбой") }}, true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)
	time.Sleep(150 * time.Millisecond)
	if n := runs.Load(); n != 2 {
		t.Fatalf("за 150 мс ожидалось 2 запуска (пауза 10 мс, затем 300 мс), было %d", n)
	}
}

func TestIsRunningUnknownModule(t *testing.T) {
	if New(quiet()).IsRunning("nope") {
		t.Fatal("незарегистрированный модуль не может работать")
	}
}
