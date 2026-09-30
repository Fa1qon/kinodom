package rutracker

import (
	"context"
	"net/http"
	"net/url"
	"time"
)

// Session — пропуск Cloudflare и вход на одном зеркале: cookie (только имя и значение) и User-Agent,
// к которому привязан пропуск. Переживает перезапуск службы (хвост 5c, спека этапа 11a, раздел 8):
// без неё после перезапуска — новый проход Edge и новый вход.
type Session struct {
	Mirror    string
	Cookies   []*http.Cookie
	UserAgent string
	Login     string // под каким логином сохранена ("" — гостем): чужая не восстанавливается (хвост Х40)
	SavedAt   time.Time
}

// SessionStore — где хранится сессия (приложение — таблица tracker_state в базе, закрытой для всех,
// кроме службы и администраторов).
type SessionStore interface {
	Load(ctx context.Context, tracker string) (Session, bool, error)
	Save(ctx context.Context, tracker string, s Session) error
	Delete(ctx context.Context, tracker string) error
}

// SetSessionStore подключает хранилище и восстанавливает сессию прошлого запуска — если она того же
// зеркала. Устарела — прежний путь: проверка Cloudflare даст новый пропуск, форма входа — вход.
func (r *Rutracker) SetSessionStore(s SessionStore) {
	r.mu.Lock()
	r.sessions = s
	r.mu.Unlock()
	sess, ok, err := s.Load(context.Background(), Name)
	if err != nil {
		r.log.Warn("Rutracker: сессия прошлого запуска не прочиталась", "err", err)
		return
	}
	mirror := r.forum.Mirror()
	r.mu.Lock()
	login := r.login
	r.mu.Unlock()
	if !ok || mirror == "" || sess.Mirror != mirror || sess.Login != login {
		return // другое зеркало или другая учётная запись — прежний путь (пропуск и вход)
	}
	u, err := url.Parse(mirror + "/")
	if err != nil {
		return
	}
	cookies := make([]*http.Cookie, 0, len(sess.Cookies))
	for _, c := range sess.Cookies {
		cookies = append(cookies, &http.Cookie{Name: c.Name, Value: c.Value, Path: "/"})
	}
	r.jar.SetCookies(u, cookies)
	if sess.UserAgent != "" {
		r.forum.SetUserAgent(sess.UserAgent)
	}
	r.log.Info("Rutracker: сессия прошлого запуска восстановлена", "mirror", mirror) // без значений cookie
}

// forgetSession стирает сохранённую сессию: сменили или стёрли логин (хвост Х40).
func (r *Rutracker) forgetSession() {
	r.mu.Lock()
	store := r.sessions
	r.mu.Unlock()
	if store == nil {
		return
	}
	if err := store.Delete(context.Background(), Name); err != nil {
		r.log.Warn("Rutracker: сохранённая сессия не стёрлась", "err", err)
	}
}

// saveSession сохраняет cookie текущего зеркала и User-Agent — после пропуска Cloudflare и входа.
// Вызывать без r.mu.
func (r *Rutracker) saveSession(ctx context.Context) {
	r.mu.Lock()
	store, login := r.sessions, r.login
	r.mu.Unlock()
	mirror := r.forum.Mirror()
	if store == nil || mirror == "" {
		return
	}
	u, err := url.Parse(mirror + "/forum/")
	if err != nil {
		return
	}
	var cookies []*http.Cookie
	for _, c := range r.jar.Cookies(u) {
		cookies = append(cookies, &http.Cookie{Name: c.Name, Value: c.Value})
	}
	s := Session{Mirror: mirror, Cookies: cookies, UserAgent: r.forum.UserAgent(), Login: login, SavedAt: r.now()}
	if err := store.Save(context.WithoutCancel(ctx), Name, s); err != nil {
		r.log.Warn("Rutracker: сессия не сохранилась", "err", err)
	}
}
