package netx

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"golang.org/x/time/rate"
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
	c, err := NewClient(Options{Name: "Трекер", Mirrors: []string{m.URL, "http://" + closedAddr(t)}, Proxy: mustProxy(t, "http://"+closedAddr(t)), Rate: 1000})
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
		"зеркало без схемы": {Name: "Трекер", Mirrors: []string{"rutor.info"}},
	}
	for name, o := range cases {
		if _, err := NewClient(o); err == nil {
			t.Errorf("%s: ошибки нет", name)
		}
	}
}

// Адрес трекера не введён (этап 11a): клиент создаётся, но никуда не ходит — ни по пути, ни по
// полному адресу — и отвечает ErrNotConfigured.
func TestClientWithoutMirrorsIsNotConfigured(t *testing.T) {
	s := newSite(t, page(trackerPage))
	c, err := NewClient(Options{Name: "Трекер", ExtraHosts: []string{hostOf(s.URL)}, Rate: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if c.Mirror() != "" || c.Configured() {
		t.Fatalf("зеркало %q, настроен %v", c.Mirror(), c.Configured())
	}
	for _, path := range []string{"/browse", s.URL + "/download/1"} {
		if _, err := c.Get(context.Background(), path); !errors.Is(err, ErrNotConfigured) {
			t.Errorf("Get(%s): %v", path, err)
		}
	}
	if s.hits.Load() != 0 {
		t.Fatal("клиент без адреса сходил в сеть")
	}
}

// Адрес поменяли в пульте — следующий запрос идёт на новый сайт; прежний хост больше не «свой»,
// новый дополнительный — свой.
func TestSetMirrorsSwitchesOnTheFly(t *testing.T) {
	old, fresh, dl := newSite(t, page(trackerPage)), newSite(t, page(trackerPage)), newSite(t, page(trackerPage))
	c := newTestClient(t, old.URL)
	if _, err := c.Get(context.Background(), "/a"); err != nil {
		t.Fatal(err)
	}
	if err := c.SetMirrors([]string{fresh.URL + "/"}, hostOf(dl.URL)); err != nil {
		t.Fatal(err)
	}
	if c.Mirror() != fresh.URL {
		t.Fatalf("зеркало %q", c.Mirror())
	}
	if _, err := c.Get(context.Background(), "/b"); err != nil || fresh.hits.Load() != 1 || old.hits.Load() != 1 {
		t.Fatalf("err %v, новый %d, старый %d", err, fresh.hits.Load(), old.hits.Load())
	}
	if _, err := c.Get(context.Background(), dl.URL+"/download/1"); err != nil {
		t.Fatalf("дополнительный хост: %v", err)
	}
	if _, err := c.Get(context.Background(), old.URL+"/x"); err == nil {
		t.Fatal("прежний сайт всё ещё свой")
	}
	if err := c.SetMirrors([]string{"не адрес"}); err == nil {
		t.Fatal("мусор принят")
	}
	if c.Mirror() != fresh.URL {
		t.Fatalf("после отказа зеркало %q", c.Mirror())
	}
	if err := c.SetMirrors(nil); err != nil || c.Configured() {
		t.Fatalf("сброс адреса: %v, настроен %v", err, c.Configured())
	}
	if _, err := c.Get(context.Background(), "/c"); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("после сброса: %v", err)
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
		"HTTP-прокси, 407 на CONNECT":        {Mirrors: []string{"https://rutor.example", "https://rutor2.example"}, Proxy: mustProxy(t, proxy407.URL)},
		"HTTP-прокси, 407 на обычный запрос": {Mirrors: []string{"http://rutor.example", "http://rutor2.example"}, Proxy: mustProxy(t, proxy407.URL)},
		"SOCKS5 не принимает логин":          {Mirrors: []string{"https://rutor.example", "https://rutor2.example"}, Proxy: mustProxy(t, "socks5://user:pass@"+socksRefusing(t))},
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

// У трекеров без Edge (Rutor) проверка Cloudflare на зеркале — повод перейти на другое.
func TestChallengeCanMeanMirrorDown(t *testing.T) {
	cf := newSite(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cf-Mitigated", "challenge")
		w.WriteHeader(http.StatusForbidden)
	})
	good := newSite(t, page(trackerPage))
	c, err := NewClient(Options{Name: "Трекер", Mirrors: []string{cf.URL, good.URL}, Classify: testClassify, Rate: 1000, ChallengeIsMirrorDown: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Get(context.Background(), "/browse"); err != nil {
		t.Fatalf("ожидался переход на рабочее зеркало, получено %v", err)
	}
	if c.Mirror() != good.URL {
		t.Fatalf("текущее зеркало %s", c.Mirror())
	}
}

func TestJarKeepsCookies(t *testing.T) {
	m := newSite(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/set" {
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "42", Path: "/"})
		} else if c, err := r.Cookie("session"); err != nil || c.Value != "42" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		page(trackerPage)(w, r)
	})
	jar, _ := cookiejar.New(nil)
	c, err := NewClient(Options{Name: "Трекер", Mirrors: []string{m.URL}, Jar: jar, Rate: 1000})
	if err != nil {
		t.Fatal(err)
	}
	c.Get(context.Background(), "/set")
	if p, err := c.Get(context.Background(), "/check"); err != nil || p.Status != 200 {
		t.Fatalf("cookie не отправлена: %v %v", p, err)
	}
}

func TestPostSendsFormAndFollowsRedirect(t *testing.T) {
	var got string
	m := newSite(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login":
			b, _ := io.ReadAll(r.Body)
			got = r.Method + " " + r.Header.Get("Content-Type") + " " + string(b)
			http.Redirect(w, r, "/index", http.StatusFound)
		default:
			page(trackerPage)(w, r)
		}
	})
	c := newTestClient(t, m.URL)
	p, err := c.Post(context.Background(), "/login", "a=1&b=%C2%F5", WithoutClassify())
	if err != nil {
		t.Fatal(err)
	}
	if got != "POST application/x-www-form-urlencoded a=1&b=%C2%F5" || p.URL.Path != "/index" {
		t.Fatalf("запрос %q, итоговая страница %s", got, p.URL.Path)
	}
}

// Ответ разбирает сам вызывающий: признаки трекера (здесь «нужен вход») не применяются.
func TestWithoutClassifyReturnsPage(t *testing.T) {
	m := newSite(t, page(trackerPage))
	c := newTestClient(t, m.URL)
	if _, err := c.Get(context.Background(), "/login"); !errors.Is(err, ErrLoginRequired) {
		t.Fatalf("без опции: %v", err)
	}
	if p, err := c.Get(context.Background(), "/login", WithoutClassify()); err != nil || p.URL.Path != "/login" {
		t.Fatalf("с опцией: %v", err)
	}
}

// Два клиента одного трекера (форум и API) делят ограничитель «1 в секунду».
func TestSharedLimiter(t *testing.T) {
	a, b := newSite(t, page(trackerPage)), newSite(t, page(trackerPage))
	lim := rate.NewLimiter(10, 1)
	ca, _ := NewClient(Options{Name: "Форум", Mirrors: []string{a.URL}, Limiter: lim})
	cb, _ := NewClient(Options{Name: "API", Mirrors: []string{b.URL}, Limiter: lim})
	start := time.Now()
	for range 2 {
		ca.Get(context.Background(), "/x")
		cb.Get(context.Background(), "/x")
	}
	if d := time.Since(start); d < 250*time.Millisecond {
		t.Fatalf("4 запроса через общий ограничитель 10/с прошли за %v", d)
	}
}

// Форму (вход на трекер) не отправляют второй раз: ни повтором после таймаута, ни на другое
// зеркало — у Rutracker каждая лишняя попытка входа приближает капчу.
func TestPostIsNotRepeated(t *testing.T) {
	var posts atomic.Int32
	slow := newSite(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			posts.Add(1)
			io.ReadAll(r.Body) // пока тело не прочитано, сервер не замечает ухода клиента
		}
		hang(w, r)
	})
	other := newSite(t, page(trackerPage))
	c, err := NewClient(Options{Name: "Трекер", Mirrors: []string{slow.URL, other.URL}, Classify: testClassify, Rate: 1000, Timeout: 200 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Post(context.Background(), "/login", "a=1", WithoutClassify()); err == nil {
		t.Fatal("ошибки нет")
	}
	if posts.Load() != 1 || other.hits.Load() != 0 {
		t.Fatalf("форма ушла %d раз, на другое зеркало — %d", posts.Load(), other.hits.Load())
	}
}

// Очередь ограничителя не успевает до срока запроса — для вызывающего это «вышло время», а не
// «трекер недоступен»: каталог (этап 5b) отличает одно от другого через errors.Is.
func TestLimiterWaitPastDeadlineIsDeadline(t *testing.T) {
	s := newSite(t, page(trackerPage))
	c, err := NewClient(Options{Name: "Трекер", Mirrors: []string{s.URL}, Classify: testClassify, Rate: 0.2}) // 1 запрос в 5 с
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Get(context.Background(), "/a"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err = c.Get(ctx, "/b")
	if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrTrackerDown) {
		t.Fatalf("ожидалось «вышло время», получено %v", err)
	}
}

// Причины «зеркало недоступно» — короткие и по-русски: они уходят в «Проблемы» (этап 5b).
func TestNetReasonIsShortRussian(t *testing.T) {
	wrap := func(err error) error { return &url.Error{Op: "Get", URL: "https://rutor.info/browse", Err: err} }
	cases := []struct {
		err  error
		want string
	}{
		{wrap(&net.OpError{Op: "read", Net: "tcp", Err: os.NewSyscallError("wsarecv", syscall.WSAECONNRESET)}), "соединение сброшено"},
		{wrap(&net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET}), "соединение сброшено"},
		{wrap(io.EOF), "соединение закрылось без ответа"},
		{wrap(io.ErrUnexpectedEOF), "соединение закрылось без ответа"},
		{wrap(tls.AlertError(40)), "ошибка TLS"},
		{wrap(errors.New("tls: first record does not look like a TLS handshake")), "ошибка TLS"},
		{wrap(errors.New("stopped after 10 redirects")), "слишком много перенаправлений"},
		{wrap(&net.DNSError{Err: "no such host", Name: "rutor.info"}), "адрес не найден (DNS)"},
		{wrap(errors.New("что-то невиданное")), "сетевая ошибка"},
	}
	for _, c := range cases {
		if got := netReason(c.err); got != c.want {
			t.Errorf("netReason(%v) = %q, нужно %q", c.err, got, c.want)
		}
	}
}

// Зеркало из настроек со «/» на конце: адрес зеркала — без него, иначе cookie сессии ищутся
// по «https://…//forum/», а Edge открывает «//forum/…» (ревью этапа 4).
func TestMirrorTrailingSlashIsTrimmed(t *testing.T) {
	s := newSite(t, page(trackerPage))
	mirrors := []string{s.URL + "/"}
	c, err := NewClient(Options{Name: "Трекер", Mirrors: mirrors, Classify: testClassify, Rate: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if c.Mirror() != s.URL {
		t.Fatalf("зеркало %q", c.Mirror())
	}
	if mirrors[0] != s.URL+"/" {
		t.Fatal("NewClient изменил список вызывающего")
	}
}

// Edge обновился — пропуск привязан к новому UA, и клиент переключается на него.
func TestSetUserAgent(t *testing.T) {
	var got atomic.Value
	s := newSite(t, func(w http.ResponseWriter, r *http.Request) {
		got.Store(r.UserAgent())
		page(trackerPage)(w, r)
	})
	c, err := NewClient(Options{Name: "Трекер", Mirrors: []string{s.URL}, Classify: testClassify, Rate: 1000, UserAgent: "Edg/100"})
	if err != nil {
		t.Fatal(err)
	}
	for _, ua := range []string{"Edg/100", "Edg/200"} {
		if ua == "Edg/200" {
			c.SetUserAgent(ua)
		}
		if _, err := c.Get(context.Background(), "/x"); err != nil {
			t.Fatal(err)
		}
		if got.Load() != ua {
			t.Fatalf("UA %v, нужно %s", got.Load(), ua)
		}
	}
}
