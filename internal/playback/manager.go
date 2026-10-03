package playback

import (
	"context"
	"errors"
	"sync"
	"time"
)

// MaxPlayers — плееров одновременно (спека 18, 3.3): каждый держит процесс ffmpeg.
const MaxPlayers = 3

// ErrBusy — плееров уже MaxPlayers.
var ErrBusy = errors.New("Сейчас смотрят на трёх устройствах — закройте один плеер")

// Manager — процессы плееров: на плеер (sid) — один процесс видео и один субтитров.
type Manager struct {
	max  int
	mu   sync.Mutex
	runs map[string]*slot // kind/sid
}

type slot struct {
	kind   string
	cancel context.CancelFunc
	done   chan struct{}
}

func NewManager(max int) *Manager { return &Manager{max: max, runs: map[string]*slot{}} }

// Acquire — место для процесса kind ("video" или "subs") плеера sid. Прежний процесс того же плеера и вида
// получает отмену, и место ждёт его конца (не дольше 3 с): перемотка и смена озвучки — не новое место.
// Видео — не больше max разных плееров (ErrBusy). release — один раз, повторный — ничего.
func (m *Manager) Acquire(kind, sid string, cancel context.CancelFunc) (func(), error) {
	key := kind + "/" + sid
	m.mu.Lock()
	// Пока у плеера есть процесс этого вида — отменить и дождаться. В цикле: одновременные запуски одного плеера
	// (перемотка поверх автоповтора) ждут друг друга, и живым остаётся один (ревью 18А, Important 1).
	for old := m.runs[key]; old != nil; old = m.runs[key] {
		m.mu.Unlock()
		old.cancel()
		select {
		case <-old.done:
		case <-time.After(3 * time.Second):
		}
		m.mu.Lock()
		if m.runs[key] == old {
			delete(m.runs, key) // не дождались — место всё равно забираем
		}
	}
	if kind == "video" {
		n := 0
		for k, s := range m.runs {
			if s.kind == "video" && k != key {
				n++
			}
		}
		if n >= m.max {
			m.mu.Unlock()
			return nil, ErrBusy
		}
	}
	s := &slot{kind: kind, cancel: cancel, done: make(chan struct{})}
	m.runs[key] = s
	m.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			m.mu.Lock()
			if m.runs[key] == s {
				delete(m.runs, key)
			}
			m.mu.Unlock()
			close(s.done)
		})
	}, nil
}

// Active — сколько плееров смотрят (процессов видео).
func (m *Manager) Active() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, s := range m.runs {
		if s.kind == "video" {
			n++
		}
	}
	return n
}
