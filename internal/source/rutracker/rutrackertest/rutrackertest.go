// Package rutrackertest — страницы и ответы Rutracker, снятые вживую 2026-09-28
// (spikes/edge-cloudflare/testdata), и фейковый Rutracker на них. Только для тестов.
package rutrackertest

import (
	"embed"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
)

//go:embed testdata
var files embed.FS

// Page — образец testdata/<name> как есть.
func Page(t testing.TB, name string) []byte {
	t.Helper()
	b, err := files.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// CP1251 — образец в windows-1251, как его отдаёт Rutracker: *.dom.html и *.src.html сняты
// из браузера в UTF-8 — перекодируем; *.raw-cp1251.html — байты ответа как есть.
func CP1251(t testing.TB, name string) []byte {
	t.Helper()
	b := Page(t, name)
	if strings.Contains(name, ".raw-cp1251.") {
		return b
	}
	out, err := encoding.ReplaceUnsupported(charmap.Windows1251.NewEncoder()).Bytes(b)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

const (
	SessionCookie = "bb_session"
	sessionValue  = "fake-session"
)

// PassCookie — cookie, которую выдаёт пройденная проверка Cloudflare.
func PassCookie() *http.Cookie { return &http.Cookie{Name: "cf_clearance", Value: "ok"} }

// Server — фейковый Rutracker на образцах.
//
//	Forum: /forum/viewtopic.php?t=6914565 → topic.src.html; другие t — «Тема не найдена»
//	       /forum/tracker.php — без сессии редирект на login.php, с сессией — search-f2076-seeds
//	       /forum/login.php — GET: форма; POST: верные Login/Password → cookie bb_session и
//	       редирект на index.php (login-result), иначе — login-wrong (ошибка и капча)
//	API:   /v1/static/cat_forum_tree, /v1/static/pvc/f/{id} (2076 и 56 — образцы, иначе пусто)
//	Feed:  /atom/f/{id}.atom (313 — образец, иначе пустая лента)
type Server struct {
	Forum, API, Feed *httptest.Server
	Login, Password  string // верные логин и пароль; пусто — вход всегда неудачен
	NeedPass         bool   // форум отвечает проверкой Cloudflare, пока нет cookie cf_clearance=ok

	pages     map[string][]byte
	mu        sync.Mutex
	hits      map[string]int
	logins    int
	lastQuery string
}

func NewServer(t testing.TB) *Server {
	t.Helper()
	s := &Server{pages: map[string][]byte{}, hits: map[string]int{}}
	for _, n := range []string{"cloudflare-challenge.html", "topic.src.html", "topic-missing-99999999.raw-cp1251.html",
		"search-f2076-seeds.raw-cp1251.html", "login-form.dom.html", "login-result.dom.html", "login-wrong.raw-cp1251.html"} {
		s.pages[n] = CP1251(t, n)
	}
	s.Forum = httptest.NewServer(http.HandlerFunc(s.forum))
	t.Cleanup(s.Forum.Close)
	s.API = httptest.NewServer(http.HandlerFunc(s.api))
	t.Cleanup(s.API.Close)
	s.Feed = httptest.NewServer(http.HandlerFunc(s.feed))
	t.Cleanup(s.Feed.Close)
	return s
}

// Hits — сколько раз запрашивали путь (на любом из трёх серверов).
func (s *Server) Hits(path string) int { s.mu.Lock(); defer s.mu.Unlock(); return s.hits[path] }

// Logins — сколько было POST на login.php.
func (s *Server) Logins() int { s.mu.Lock(); defer s.mu.Unlock(); return s.logins }

// LastQuery — последний поисковый запрос nm, раскодированный из windows-1251.
func (s *Server) LastQuery() string { s.mu.Lock(); defer s.mu.Unlock(); return s.lastQuery }

func (s *Server) count(path string) { s.mu.Lock(); s.hits[path]++; s.mu.Unlock() }

func (s *Server) html(w http.ResponseWriter, name string) {
	w.Header().Set("Content-Type", "text/html; charset=Windows-1251")
	w.Write(s.pages[name])
}

func (s *Server) forum(w http.ResponseWriter, r *http.Request) {
	s.count(r.URL.Path)
	if c, err := r.Cookie("cf_clearance"); s.NeedPass && (err != nil || c.Value != "ok") {
		w.Header().Set("Cf-Mitigated", "challenge")
		w.Header().Set("Content-Type", "text/html; charset=UTF-8")
		w.WriteHeader(http.StatusForbidden)
		w.Write(s.pages["cloudflare-challenge.html"])
		return
	}
	c, err := r.Cookie(SessionCookie)
	session := err == nil && c.Value == sessionValue
	dec := charmap.Windows1251.NewDecoder()
	switch r.URL.Path {
	case "/forum/viewtopic.php":
		if r.URL.Query().Get("t") == "6914565" {
			s.html(w, "topic.src.html")
			return
		}
		s.html(w, "topic-missing-99999999.raw-cp1251.html")
	case "/forum/tracker.php":
		if !session {
			http.Redirect(w, r, "/forum/login.php?redirect=tracker.php", http.StatusFound)
			return
		}
		q, _ := dec.String(r.URL.Query().Get("nm"))
		s.mu.Lock()
		s.lastQuery = q
		s.mu.Unlock()
		s.html(w, "search-f2076-seeds.raw-cp1251.html")
	case "/forum/login.php":
		if r.Method != http.MethodPost {
			s.html(w, "login-form.dom.html")
			return
		}
		s.mu.Lock()
		s.logins++
		s.mu.Unlock()
		r.ParseForm()
		user, _ := dec.String(r.PostForm.Get("login_username"))
		pass, _ := dec.String(r.PostForm.Get("login_password"))
		if s.Login != "" && user == s.Login && pass == s.Password {
			http.SetCookie(w, &http.Cookie{Name: SessionCookie, Value: sessionValue, Path: "/forum/"})
			http.Redirect(w, r, "/forum/index.php", http.StatusFound)
			return
		}
		s.html(w, "login-wrong.raw-cp1251.html")
	case "/forum/index.php":
		s.html(w, "login-result.dom.html")
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) api(w http.ResponseWriter, r *http.Request) {
	s.count(r.URL.Path)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	switch {
	case r.URL.Path == "/v1/static/cat_forum_tree":
		b, _ := files.ReadFile("testdata/api-cat_forum_tree.json")
		w.Write(b)
	case strings.HasPrefix(r.URL.Path, "/v1/static/pvc/f/"):
		if b, err := files.ReadFile("testdata/api-pvc-f" + strings.TrimPrefix(r.URL.Path, "/v1/static/pvc/f/") + ".json"); err == nil {
			w.Write(b)
			return
		}
		io.WriteString(w, `{"result":{}}`)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) feed(w http.ResponseWriter, r *http.Request) {
	s.count(r.URL.Path)
	w.Header().Set("Content-Type", "application/atom+xml; charset=utf-8")
	id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/atom/f/"), ".atom")
	if b, err := files.ReadFile("testdata/atom-f" + id + ".atom"); err == nil {
		w.Write(b)
		return
	}
	io.WriteString(w, `<?xml version="1.0" encoding="utf-8"?><feed xmlns="http://www.w3.org/2005/Atom"><title>пусто</title></feed>`)
}
