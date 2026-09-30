package rutracker

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"

	"kinodom/internal/source/rutracker/rutrackertest"
)

// memSessions — хранилище сессий в памяти (в приложении — таблица tracker_state).
type memSessions struct {
	mu    sync.Mutex
	saved map[string]Session
	saves int
}

func (m *memSessions) Load(_ context.Context, tracker string) (Session, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.saved[tracker]
	return s, ok, nil
}

func (m *memSessions) Save(_ context.Context, tracker string, s Session) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.saved == nil {
		m.saved = map[string]Session{}
	}
	m.saved[tracker] = s
	m.saves++
	return nil
}

func (m *memSessions) Delete(_ context.Context, tracker string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.saved, tracker)
	return nil
}

// Сменили или стёрли логин (хвост Х40) — сохранённая сессия прежней учётной записи стирается и из
// хранилища: после перезапуска она не вернулась бы под чужим логином.
func TestSessionDroppedOnLoginChange(t *testing.T) {
	s := rutrackertest.NewServer(t)
	s.Login, s.Password = "user", "pass"
	store := &memSessions{}
	r := newRutracker(t, s, func(o *Options) { o.Login, o.Password = "user", "pass" })
	r.SetSessionStore(store)
	if _, err := r.Details(ctx, "6914565"); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.saved[Name]; !ok || store.saved[Name].Login != "user" {
		t.Fatalf("сессия не сохранилась под логином: %+v", store.saved[Name])
	}
	r.SetCredentials("other", "pass2")
	if _, ok := store.saved[Name]; ok {
		t.Fatal("сессия прежнего логина осталась в хранилище")
	}
}

// Сохранённая под другим логином сессия при старте не восстанавливается (хвост Х40).
func TestSessionOtherLoginNotRestored(t *testing.T) {
	s := rutrackertest.NewServer(t)
	store := &memSessions{saved: map[string]Session{Name: {Mirror: s.Forum.URL, Login: "user",
		Cookies: []*http.Cookie{{Name: rutrackertest.SessionCookie, Value: "чужая"}}}}}
	r := newRutracker(t, s, func(o *Options) { o.Login, o.Password = "other", "pass" })
	r.SetSessionStore(store)
	u, _ := url.Parse(s.Forum.URL + "/forum/")
	for _, c := range r.jar.Cookies(u) {
		if c.Name == rutrackertest.SessionCookie {
			t.Fatalf("восстановлена сессия чужого логина: %+v", c)
		}
	}
}

// Пропуск Cloudflare и вход сохраняются; новый процесс (перезапуск службы) берёт их из хранилища —
// без нового прохода Edge и без входа (хвост 5c, спека этапа 11a, раздел 8).
func TestSessionSurvivesRestart(t *testing.T) {
	s := rutrackertest.NewServer(t)
	s.NeedPass, s.Login, s.Password = true, "user", "pass"
	store := &memSessions{}
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))

	p1 := &fakePasser{ua: "UA-Edge-1"}
	r1 := newRutracker(t, s, func(o *Options) { o.Passer, o.Login, o.Password, o.Log = p1, "user", "pass", log })
	r1.SetSessionStore(store)
	if _, err := r1.Details(ctx, "6914565"); err != nil {
		t.Fatal(err)
	}
	if p1.calls.Load() != 1 || s.Logins() != 1 || store.saves == 0 {
		t.Fatalf("первый запуск: пропусков %d, входов %d, сохранений %d", p1.calls.Load(), s.Logins(), store.saves)
	}
	saved := store.saved[Name]
	if saved.UserAgent != "UA-Edge-1" || saved.Mirror != s.Forum.URL || !hasCookie(saved, "cf_clearance") || !hasCookie(saved, rutrackertest.SessionCookie) {
		t.Fatalf("сохранено: %+v", saved)
	}

	p2 := &fakePasser{ua: "UA-Edge-2"}
	r2 := newRutracker(t, s, func(o *Options) { o.Passer, o.Login, o.Password = p2, "user", "pass" })
	r2.SetSessionStore(store)
	if _, err := r2.Details(ctx, "6914565"); err != nil {
		t.Fatal(err)
	}
	if p2.calls.Load() != 0 || s.Logins() != 1 {
		t.Fatalf("после перезапуска: пропусков %d, входов %d", p2.calls.Load(), s.Logins())
	}
	if s.LastUserAgent() != "UA-Edge-1" {
		t.Fatalf("UA после перезапуска %q — пропуск привязан к UA браузера", s.LastUserAgent())
	}
	for _, secret := range []string{"cf_clearance=ok", cookieValue(saved, rutrackertest.SessionCookie)} {
		if secret != "" && strings.Contains(logs.String(), secret) {
			t.Fatalf("в журнале секрет %q", secret)
		}
	}
}

// Сохранённая сессия устарела — прежний путь: новый пропуск Edge и вход (форум просит войти).
func TestStaleSessionFallsBack(t *testing.T) {
	s := rutrackertest.NewServer(t)
	s.NeedPass, s.Login, s.Password, s.TopicNeedsLogin = true, "user", "pass", true
	store := &memSessions{saved: map[string]Session{Name: {Mirror: s.Forum.URL, UserAgent: "UA-old",
		Cookies: []*http.Cookie{{Name: "cf_clearance", Value: "expired"}, {Name: rutrackertest.SessionCookie, Value: "old"}}}}}
	p := &fakePasser{ua: "UA-new"}
	r := newRutracker(t, s, func(o *Options) { o.Passer, o.Login, o.Password = p, "user", "pass" })
	r.SetSessionStore(store)
	if _, err := r.Details(ctx, "6914565"); err != nil {
		t.Fatal(err)
	}
	if p.calls.Load() != 1 || s.Logins() != 1 || store.saved[Name].UserAgent != "UA-new" {
		t.Fatalf("пропусков %d, входов %d, сохранено %+v", p.calls.Load(), s.Logins(), store.saved[Name])
	}
}

// Сессия другого зеркала (адрес поменяли) не восстанавливается.
func TestSessionOfOtherMirrorIgnored(t *testing.T) {
	s := rutrackertest.NewServer(t)
	s.NeedPass = true
	store := &memSessions{saved: map[string]Session{Name: {Mirror: "https://old-mirror.example", UserAgent: "UA-old",
		Cookies: []*http.Cookie{{Name: "cf_clearance", Value: "ok"}}}}}
	p := &fakePasser{}
	r := newRutracker(t, s, func(o *Options) { o.Passer = p })
	r.SetSessionStore(store)
	if _, err := r.Details(ctx, "6914565"); err != nil {
		t.Fatal(err)
	}
	if p.calls.Load() != 1 {
		t.Fatalf("пропусков %d: сессия чужого зеркала применилась", p.calls.Load())
	}
}

func hasCookie(s Session, name string) bool { return cookieValue(s, name) != "" }

func cookieValue(s Session, name string) string {
	for _, c := range s.Cookies {
		if c.Name == name {
			return c.Value
		}
	}
	return ""
}
