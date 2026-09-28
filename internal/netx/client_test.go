package netx

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// site — фейковое зеркало, считает запросы.
type site struct {
	*httptest.Server
	hits atomic.Int32
}

func newSite(t *testing.T, h http.HandlerFunc) *site {
	t.Helper()
	s := &site{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.hits.Add(1)
		h(w, r)
	}))
	t.Cleanup(s.Close)
	return s
}

func page(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=UTF-8")
		io.WriteString(w, body)
	}
}

func status(code int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(code) }
}

// trackerPage — страница с каркасом сайта, как у Rutor.
const trackerPage = `<html><div id="logo"></div><div id="menu"></div>раздачи</html>`

// testClassify — признаки выдуманного трекера: /removed — удалена, /login — вход,
// HTML без каркаса — заглушка вместо зеркала.
func testClassify(p *Page) Verdict {
	switch {
	case p.URL.Path == "/removed":
		return Removed
	case p.URL.Path == "/login":
		return LoginRequired
	case p.Status == http.StatusOK && strings.HasPrefix(p.Header.Get("Content-Type"), "text/html") &&
		!bytes.Contains(p.Body, []byte(`id="logo"`)):
		return MirrorDown
	}
	return OK
}

func newTestClient(t *testing.T, mirrors ...string) *Client {
	t.Helper()
	c, err := NewClient(Options{Name: "Трекер", Mirrors: mirrors, Classify: testClassify, Rate: 1000, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestGetUsesFirstMirror(t *testing.T) {
	a, b := newSite(t, page(trackerPage)), newSite(t, page(trackerPage))
	c := newTestClient(t, a.URL, b.URL)
	p, err := c.Get(context.Background(), "/browse")
	if err != nil {
		t.Fatal(err)
	}
	if string(p.Body) != trackerPage || p.Status != 200 || p.URL.Path != "/browse" {
		t.Fatalf("страница: %d %s %q", p.Status, p.URL, p.Body)
	}
	if a.hits.Load() != 1 || b.hits.Load() != 0 || c.Mirror() != a.URL {
		t.Fatalf("запросы %d/%d, текущее зеркало %s", a.hits.Load(), b.hits.Load(), c.Mirror())
	}
}

func TestMirrorDownMovesToNextAndRemembers(t *testing.T) {
	foreign := newSite(t, page(trackerPage))
	cases := map[string]func(t *testing.T) string{
		"ответ 502": func(t *testing.T) string { return newSite(t, status(502)).URL },
		"ответ 451": func(t *testing.T) string { return newSite(t, status(451)).URL },
		"заглушка":  func(t *testing.T) string { return newSite(t, page("<html>Домен продаётся</html>")).URL },
		"чужой сайт": func(t *testing.T) string {
			return newSite(t, func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, foreign.URL+r.URL.Path, http.StatusFound)
			}).URL
		},
		"TLS": func(t *testing.T) string {
			s := httptest.NewUnstartedServer(page(trackerPage))
			s.Config.ErrorLog = log.New(io.Discard, "", 0)
			s.StartTLS() // сертификат, которому клиент не доверяет
			t.Cleanup(s.Close)
			return s.URL
		},
		"не подключается": func(t *testing.T) string { return "http://" + closedAddr(t) },
	}
	for name, bad := range cases {
		t.Run(name, func(t *testing.T) {
			good := newSite(t, page(trackerPage))
			c := newTestClient(t, bad(t), good.URL)
			for i := 1; i <= 2; i++ {
				if _, err := c.Get(context.Background(), "/browse"); err != nil {
					t.Fatalf("запрос %d: %v", i, err)
				}
			}
			// Второй запрос сразу идёт на рабочее зеркало: оно запомнено.
			if good.hits.Load() != 2 || c.Mirror() != good.URL {
				t.Fatalf("на рабочее зеркало %d запросов, текущее %s", good.hits.Load(), c.Mirror())
			}
		})
	}
}

func TestChallengeKeepsMirror(t *testing.T) {
	challenge, err := os.ReadFile("testdata/cloudflare-challenge.html")
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]http.HandlerFunc{
		"заголовок cf-mitigated": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cf-Mitigated", "challenge")
			w.WriteHeader(http.StatusForbidden)
		},
		"страница проверки, 403": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			w.Write(challenge)
		},
		"страница проверки, 503": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write(challenge)
		},
	}
	for name, h := range cases {
		t.Run(name, func(t *testing.T) {
			cf, next := newSite(t, h), newSite(t, page(trackerPage))
			_, err := newTestClient(t, cf.URL, next.URL).Get(context.Background(), "/viewtopic")
			if !errors.Is(err, ErrChallenge) {
				t.Fatalf("ожидалась ErrChallenge, получено %v", err)
			}
			if next.hits.Load() != 0 {
				t.Fatal("из-за проверки Cloudflare сменилось зеркало")
			}
		})
	}
}

func TestTrackerVerdicts(t *testing.T) {
	m, next := newSite(t, page(trackerPage)), newSite(t, page(trackerPage))
	c := newTestClient(t, m.URL, next.URL)
	if _, err := c.Get(context.Background(), "/removed"); !errors.Is(err, ErrRemoved) {
		t.Errorf("/removed: %v", err)
	}
	if _, err := c.Get(context.Background(), "/login"); !errors.Is(err, ErrLoginRequired) {
		t.Errorf("/login: %v", err)
	}
	if next.hits.Load() != 0 {
		t.Error("живое зеркало сменилось")
	}
}

func TestAllMirrorsDownNamesEveryMirror(t *testing.T) {
	a, b := newSite(t, status(502)), newSite(t, page("<html>Домен продаётся</html>"))
	_, err := newTestClient(t, a.URL, b.URL).Get(context.Background(), "/browse")
	if !errors.Is(err, ErrTrackerDown) {
		t.Fatalf("ожидалась ErrTrackerDown, получено %v", err)
	}
	for _, want := range []string{"Трекер недоступен", hostOf(a.URL) + " — ответ 502", hostOf(b.URL) + " — вместо трекера"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("в тексте %q нет %q", err, want)
		}
	}
}

func TestAbsoluteURLIsNotRotated(t *testing.T) {
	m := newSite(t, page(trackerPage))
	dl := newSite(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-bittorrent")
		io.WriteString(w, "d8:announce0:e")
	})
	c, err := NewClient(Options{Name: "Трекер", Mirrors: []string{m.URL}, ExtraHosts: []string{hostOf(dl.URL)}, Classify: testClassify, Rate: 1000})
	if err != nil {
		t.Fatal(err)
	}
	p, err := c.Get(context.Background(), dl.URL+"/download/1")
	if err != nil || string(p.Body) != "d8:announce0:e" {
		t.Fatalf("полный адрес: %v", err)
	}
	if m.hits.Load() != 0 {
		t.Fatal("запрос по полному адресу ушёл на зеркало")
	}
	// Полный адрес чужого сайта — «зеркало недоступно», а не страница.
	other := newSite(t, page(trackerPage))
	if _, err := c.Get(context.Background(), other.URL+"/x"); !errors.Is(err, ErrTrackerDown) {
		t.Fatalf("чужой адрес: %v", err)
	}
}

func TestProxyDownStopsWithoutTryingMirrors(t *testing.T) {
	m := newSite(t, page(trackerPage))
	c, err := NewClient(Options{Name: "Трекер", Mirrors: []string{m.URL, "http://" + closedAddr(t)}, Proxy: "http://" + closedAddr(t), Rate: 1000})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Get(context.Background(), "/browse")
	if !errors.Is(err, ErrProxyDown) || errors.Is(err, ErrTrackerDown) {
		t.Fatalf("ожидалась только ErrProxyDown, получено %v", err)
	}
	if m.hits.Load() != 0 {
		t.Fatal("запрос дошёл до зеркала мимо прокси")
	}
}

func TestNewClientRejectsBadOptions(t *testing.T) {
	cases := map[string]Options{
		"нет зеркал":        {Name: "Трекер"},
		"зеркало без схемы": {Name: "Трекер", Mirrors: []string{"rutor.info"}},
		"прокси без схемы":  {Name: "Трекер", Mirrors: []string{"https://rutor.info"}, Proxy: "127.0.0.1:1080"},
	}
	for name, o := range cases {
		if _, err := NewClient(o); err == nil {
			t.Errorf("%s: ошибки нет", name)
		}
	}
}
