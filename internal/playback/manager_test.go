package playback

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func noop() {}

func TestManagerLimit(t *testing.T) {
	m := NewManager(3)
	var rel []func()
	for _, sid := range []string{"a", "b", "c"} {
		r, err := m.Acquire("video", sid, noop)
		if err != nil {
			t.Fatal(sid, err)
		}
		rel = append(rel, r)
	}
	if _, err := m.Acquire("video", "d", noop); !errors.Is(err, ErrBusy) || err.Error() != "Сейчас смотрят на трёх устройствах — закройте один плеер" {
		t.Fatalf("четвёртый: %v", err)
	}
	rel[0]()
	rel[0]() // повторно — ничего
	if _, err := m.Acquire("video", "d", noop); err != nil || m.Active() != 3 {
		t.Fatalf("место освободилось: %v %d", err, m.Active())
	}
}

// Перемотка — тот же плеер: прежний процесс получает отмену, новый ждёт его конца и места не занимает.
func TestManagerSameSidReplaces(t *testing.T) {
	m := NewManager(1)
	var rel1 func()
	var cancelled atomic.Bool
	rel1, err := m.Acquire("video", "a", func() {
		cancelled.Store(true)
		go func() { time.Sleep(50 * time.Millisecond); rel1() }()
	})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	rel2, err := m.Acquire("video", "a", noop)
	if err != nil || !cancelled.Load() || time.Since(start) < 40*time.Millisecond || m.Active() != 1 {
		t.Fatalf("замена: %v %v %v %d", err, cancelled.Load(), time.Since(start), m.Active())
	}
	rel1() // поздний release старого не трогает новый
	if m.Active() != 1 {
		t.Fatalf("старый снял новый: %d", m.Active())
	}
	rel2()
	if m.Active() != 0 {
		t.Fatalf("после конца: %d", m.Active())
	}
}

// Субтитры — без места в пределе, один процесс на плеер.
func TestManagerSubsNotCounted(t *testing.T) {
	m := NewManager(1)
	if _, err := m.Acquire("video", "a", noop); err != nil {
		t.Fatal(err)
	}
	var cancelled atomic.Bool
	var relS func()
	relS, err := m.Acquire("subs", "a", func() { cancelled.Store(true); relS() })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Acquire("subs", "a", noop); err != nil || !cancelled.Load() || m.Active() != 1 {
		t.Fatalf("субтитры: %v %v %d", err, cancelled.Load(), m.Active())
	}
}
