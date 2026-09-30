package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"kinodom/internal/source/rutracker"
	"kinodom/internal/store"
)

// trackerSessions — сессии трекеров в базе (таблица tracker_state): вход и пропуск Cloudflare
// переживают перезапуск службы.
type trackerSessions struct{ db *store.DB }

type storedCookie struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type storedSession struct {
	Mirror    string         `json:"mirror"`
	UserAgent string         `json:"userAgent"`
	Login     string         `json:"login"`
	Cookies   []storedCookie `json:"cookies"`
}

func (s trackerSessions) Load(ctx context.Context, tracker string) (rutracker.Session, bool, error) {
	var raw string
	var at int64
	err := s.db.R.QueryRowContext(ctx, `SELECT session, saved_at FROM tracker_state WHERE tracker = ?`, tracker).Scan(&raw, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return rutracker.Session{}, false, nil
	}
	if err != nil {
		return rutracker.Session{}, false, err
	}
	var v storedSession
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return rutracker.Session{}, false, nil // испорчена — как не было: прежний путь (Edge и вход)
	}
	out := rutracker.Session{Mirror: v.Mirror, UserAgent: v.UserAgent, Login: v.Login, SavedAt: time.UnixMilli(at)}
	for _, c := range v.Cookies {
		out.Cookies = append(out.Cookies, &http.Cookie{Name: c.Name, Value: c.Value})
	}
	return out, true, nil
}

func (s trackerSessions) Delete(ctx context.Context, tracker string) error {
	_, err := s.db.W.ExecContext(ctx, `DELETE FROM tracker_state WHERE tracker = ?`, tracker)
	return err
}

func (s trackerSessions) Save(ctx context.Context, tracker string, sess rutracker.Session) error {
	v := storedSession{Mirror: sess.Mirror, UserAgent: sess.UserAgent, Login: sess.Login}
	for _, c := range sess.Cookies {
		v.Cookies = append(v.Cookies, storedCookie{Name: c.Name, Value: c.Value})
	}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = s.db.W.ExecContext(ctx,
		`INSERT INTO tracker_state(tracker, session, saved_at) VALUES(?, ?, ?)
		 ON CONFLICT(tracker) DO UPDATE SET session = excluded.session, saved_at = excluded.saved_at`,
		tracker, string(b), sess.SavedAt.UnixMilli())
	return err
}
