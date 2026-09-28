package netx

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"net"
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

// hang — зеркало, которое молчит, пока клиент не бросит запрос.
func hang(w http.ResponseWriter, r *http.Request) {
	select {
	case <-r.Context().Done():
	case <-time.After(10 * time.Second):
	}
}

// cut — ответ обрывается посередине: обещано 1000 байт, пришло меньше, соединение закрыто.
func cut(w http.ResponseWriter) {
	w.Header().Set("Content-Length", "1000")
	w.WriteHeader(http.StatusOK)
	io.WriteString(w, `<html><div id="logo">обрыв`)
	if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
		conn.Close()
	}
}

func TestTimeoutIsRetriedOnceThenNextMirror(t *testing.T) {
	slow, good := newSite(t, hang), newSite(t, page(trackerPage))
	c, err := NewClient(Options{Name: "Трекер", Mirrors: []string{slow.URL, good.URL}, Classify: testClassify, Rate: 1000, Timeout: 200 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Get(context.Background(), "/browse"); err != nil {
		t.Fatal(err)
	}
	if slow.hits.Load() != 2 || good.hits.Load() != 1 {
		t.Fatalf("медленное зеркало: %d попыток (нужно 2), рабочее: %d", slow.hits.Load(), good.hits.Load())
	}
}

func TestCutBodyIsRetriedOnSameMirror(t *testing.T) {
	var calls atomic.Int32
	m := newSite(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			cut(w)
			return
		}
		page(trackerPage)(w, r)
	})
	next := newSite(t, page(trackerPage))
	c := newTestClient(t, m.URL, next.URL)
	p, err := c.Get(context.Background(), "/browse")
	if err != nil || string(p.Body) != trackerPage {
		t.Fatalf("после обрыва: %v", err)
	}
	if m.hits.Load() != 2 || next.hits.Load() != 0 || c.Mirror() != m.URL {
		t.Fatalf("попыток %d, на следующее зеркало %d — обрыв не должен менять зеркало", m.hits.Load(), next.hits.Load())
	}
}

func TestCutBodyTwiceMovesOn(t *testing.T) {
	m := newSite(t, func(w http.ResponseWriter, r *http.Request) { cut(w) })
	next := newSite(t, page(trackerPage))
	if _, err := newTestClient(t, m.URL, next.URL).Get(context.Background(), "/browse"); err != nil {
		t.Fatal(err)
	}
	if m.hits.Load() != 2 || next.hits.Load() != 1 {
		t.Fatalf("попыток %d/%d", m.hits.Load(), next.hits.Load())
	}
}

func TestCancelledContextStopsAtOnce(t *testing.T) {
	slow, next := newSite(t, hang), newSite(t, page(trackerPage))
	c := newTestClient(t, slow.URL, next.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := c.Get(ctx, "/browse")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ожидалась отмена, получено %v", err)
	}
	if time.Since(start) > time.Second || slow.hits.Load() != 1 || next.hits.Load() != 0 {
		t.Fatalf("после отмены: %v, попыток %d/%d", time.Since(start), slow.hits.Load(), next.hits.Load())
	}
}

func TestLimiterSpacesRequests(t *testing.T) {
	m := newSite(t, page(trackerPage))
	c, err := NewClient(Options{Name: "Трекер", Mirrors: []string{m.URL}, Rate: 10})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	for range 4 {
		if _, err := c.Get(context.Background(), "/browse"); err != nil {
			t.Fatal(err)
		}
	}
	// Первый запрос — сразу, следующие три — через 100 мс каждый.
	if d := time.Since(start); d < 250*time.Millisecond {
		t.Fatalf("4 запроса при 10/с прошли за %v", d)
	}
}

func TestWithoutLimitSkipsLimiter(t *testing.T) {
	m := newSite(t, page(trackerPage))
	c, err := NewClient(Options{Name: "Трекер", Mirrors: []string{m.URL}, Rate: 1})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	for range 4 {
		if _, err := c.Get(context.Background(), "/search", WithoutLimit()); err != nil {
			t.Fatal(err)
		}
	}
	if d := time.Since(start); d > 900*time.Millisecond {
		t.Fatalf("4 запроса вне ограничителя шли %v", d)
	}
}

func TestDefaultsFollowSpec(t *testing.T) {
	c, err := NewClient(Options{Name: "Трекер", Mirrors: []string{"https://rutor.info"}})
	if err != nil {
		t.Fatal(err)
	}
	if c.lim.Limit() != 1 || c.o.Timeout != 90*time.Second {
		t.Fatalf("по умолчанию %v запросов/с и таймаут %v; спека: 1/с и 90 с", c.lim.Limit(), c.o.Timeout)
	}
}

// socksRefusing — SOCKS5-прокси, который не принимает ни один способ входа.
func socksRefusing(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				hdr := make([]byte, 2) // версия, число способов входа
				if _, err := io.ReadFull(c, hdr); err != nil {
					return
				}
				io.ReadFull(c, make([]byte, hdr[1]))
				c.Write([]byte{5, 0xFF}) // «ни один способ не подходит»
			}()
		}
	}()
	return ln.Addr().String()
}

// Прокси ответил, но отказал (неверный логин или пароль) — это проблема прокси, а не трекера:
// зеркала не перебираются, ошибка — ErrProxyDown с понятным текстом (спека, раздел 16).
func TestProxyRefusalIsProxyProblem(t *testing.T) {
	proxy407 := newSite(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusProxyAuthRequired)
	})
	cases := map[string]Options{
		"HTTP-прокси, 407 на CONNECT":       {Mirrors: []string{"https://rutor.example", "https://rutor2.example"}, Proxy: proxy407.URL},
		"HTTP-прокси, 407 на обычный запрос": {Mirrors: []string{"http://rutor.example", "http://rutor2.example"}, Proxy: proxy407.URL},
		"SOCKS5 не принимает логин":          {Mirrors: []string{"https://rutor.example", "https://rutor2.example"}, Proxy: "socks5://user:pass@" + socksRefusing(t)},
	}
	for name, o := range cases {
		t.Run(name, func(t *testing.T) {
			o.Name, o.Rate = "Трекер", 1000
			c, err := NewClient(o)
			if err != nil {
				t.Fatal(err)
			}
			_, err = c.Get(context.Background(), "/browse")
			if !errors.Is(err, ErrProxyDown) || errors.Is(err, ErrTrackerDown) || !strings.Contains(err.Error(), "логин") {
				t.Fatalf("ожидалась ошибка прокси про логин, получено %v", err)
			}
		})
	}
}
