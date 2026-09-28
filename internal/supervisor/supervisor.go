// Package supervisor запускает модули сервера и перезапускает упавшие, чтобы сбой
// одного модуля (ошибка или паника) не останавливал остальные.
package supervisor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"strings"
	"sync"
	"time"
)

// Module — часть сервера с фоновой работой. Run работает, пока не отменят ctx;
// вернувшаяся ошибка, паника или ранний выход без ошибки считаются сбоем.
type Module interface {
	Name() string
	Run(ctx context.Context) error
}

// State — состояние модуля. Коды английские, пульт переводит их для людей.
type State string

const (
	StateRunning    State = "running"
	StateRestarting State = "restarting"
	StateDisabled   State = "disabled"
	StateStopped    State = "stopped"
)

// Status — то, что видно на странице «Состояние».
type Status struct {
	Name        string    `json:"name"`
	State       State     `json:"state"`
	Restarts    int       `json:"restarts"`
	LastError   string    `json:"lastError,omitempty"`
	LastErrorAt time.Time `json:"lastErrorAt,omitzero"`
}

type Supervisor struct {
	log         *slog.Logger
	backoff     []time.Duration
	stableAfter time.Duration
	onError     func(module, text string)

	mu      sync.Mutex
	entries []*entry
}

type entry struct {
	m       Module
	enabled bool
	st      Status
}

type Option func(*Supervisor)

// WithBackoff задаёт паузы перед перезапусками: перед 1-м, 2-м, …; последняя повторяется.
func WithBackoff(d ...time.Duration) Option { return func(s *Supervisor) { s.backoff = d } }

// WithStableAfter — сколько модуль должен проработать, чтобы паузы начались сначала.
func WithStableAfter(d time.Duration) Option { return func(s *Supervisor) { s.stableAfter = d } }

// WithErrorSink — куда сообщать о сбоях (запись в базу для страницы «Состояние»).
func WithErrorSink(f func(module, text string)) Option { return func(s *Supervisor) { s.onError = f } }

// New — сторож с паузами 1 с, 5 с, 30 с, далее раз в минуту (спека, раздел 3).
func New(log *slog.Logger, opts ...Option) *Supervisor {
	s := &Supervisor{
		log:         log,
		backoff:     []time.Duration{time.Second, 5 * time.Second, 30 * time.Second, time.Minute},
		stableAfter: 2 * time.Minute,
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Add регистрирует модуль (до вызова Run). Выключенный модуль не запускается, но виден в Status.
func (s *Supervisor) Add(m Module, enabled bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := StateStopped
	if !enabled {
		st = StateDisabled
	}
	s.entries = append(s.entries, &entry{m: m, enabled: enabled, st: Status{Name: m.Name(), State: st}})
}

// Run запускает включённые модули и возвращается, когда ctx отменён и все модули остановились.
func (s *Supervisor) Run(ctx context.Context) {
	s.mu.Lock()
	es := append([]*entry(nil), s.entries...)
	s.mu.Unlock()
	var wg sync.WaitGroup
	for _, e := range es {
		if !e.enabled {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.loop(ctx, e)
		}()
	}
	wg.Wait()
}

func (s *Supervisor) loop(ctx context.Context, e *entry) {
	attempt := 0
	for {
		s.setState(e, StateRunning)
		started := time.Now()
		err := runSafely(ctx, e.m)
		if ctx.Err() != nil {
			s.setState(e, StateStopped)
			return
		}
		if err == nil {
			err = errors.New("модуль завершился, хотя его не останавливали")
		}
		if time.Since(started) >= s.stableAfter {
			attempt = 0
		}
		delay := s.backoff[min(attempt, len(s.backoff)-1)]
		attempt++
		s.fail(e, err, delay)
		select {
		case <-ctx.Done():
			s.setState(e, StateStopped)
			return
		case <-time.After(delay):
		}
	}
}

// runSafely превращает панику модуля в ошибку — иначе она уронила бы весь процесс.
func runSafely(ctx context.Context, m Module) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("паника: %v\n%s", r, debug.Stack())
		}
	}()
	return m.Run(ctx)
}

func (s *Supervisor) fail(e *entry, err error, delay time.Duration) {
	short, _, _ := strings.Cut(err.Error(), "\n") // стек — только в журнал, людям — первая строка
	s.mu.Lock()
	e.st.State = StateRestarting
	e.st.Restarts++
	e.st.LastError = short
	e.st.LastErrorAt = time.Now()
	name := e.st.Name
	s.mu.Unlock()
	s.log.Error("сбой модуля, перезапуск", "module", name, "delay", delay, "err", err)
	if s.onError != nil {
		s.onError(name, short)
	}
}

func (s *Supervisor) setState(e *entry, st State) {
	s.mu.Lock()
	e.st.State = st
	s.mu.Unlock()
}

// Status — копия состояний всех модулей в порядке регистрации.
func (s *Supervisor) Status() []Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Status, len(s.entries))
	for i, e := range s.entries {
		out[i] = e.st
	}
	return out
}

// IsRunning — модуль зарегистрирован и сейчас работает.
func (s *Supervisor) IsRunning(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.entries {
		if e.st.Name == name {
			return e.st.State == StateRunning
		}
	}
	return false
}
