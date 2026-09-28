package edge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chromedp/cdproto/network"
)

func needEdge(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("-short: без запуска Edge")
	}
	if ExecPath() == "" {
		t.Skip("Edge не установлен")
	}
}

// profileDir — папка профиля; Edge ещё мгновение держит файлы после выхода, поэтому удаление —
// с повтором и без провала теста.
func profileDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "kinodom-edge-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for i := 0; i < 50 && os.RemoveAll(dir) != nil; i++ {
			time.Sleep(200 * time.Millisecond)
		}
	})
	return dir
}

// site — локальный сайт: /page ставит cookie и отдаёт обычную страницу; /challenge — страница
// «Just a moment...», через секунду сама переходит на /page; /endless — проверка без конца.
func site(t *testing.T, pageHits *atomic.Int32) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch r.URL.Path {
		case "/page":
			pageHits.Add(1)
			http.SetCookie(w, &http.Cookie{Name: "cf_clearance", Value: "secret-value", Path: "/"})
			fmt.Fprint(w, `<html><head><title>Раздача / Release</title></head><body>ok</body></html>`)
		case "/challenge":
			fmt.Fprint(w, `<html><head><title>Just a moment...</title><meta http-equiv="refresh" content="1;url=/page"></head><body>…</body></html>`)
		default:
			fmt.Fprint(w, `<html><head><title>Один момент…</title></head><body>…</body></html>`)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func hasCookie(cs []*http.Cookie, name, value string) bool {
	for _, c := range cs {
		if c.Name == name && c.Value == value {
			return true
		}
	}
	return false
}

func TestPassReturnsSiteCookies(t *testing.T) {
	needEdge(t)
	var hits atomic.Int32
	s := site(t, &hits)
	cs, err := New(Options{ProfileDir: profileDir(t)}).Pass(context.Background(), s.URL+"/page")
	if err != nil || !hasCookie(cs, "cf_clearance", "secret-value") {
		t.Fatalf("cookie %v, %v", cs, err)
	}
}

// Заголовок со «/» (как у раздач «Название / Original») — не признак загрузки.
func TestPassWaitsOutChallenge(t *testing.T) {
	needEdge(t)
	var hits atomic.Int32
	s := site(t, &hits)
	start := time.Now()
	cs, err := New(Options{ProfileDir: profileDir(t)}).Pass(context.Background(), s.URL+"/challenge")
	if err != nil || !hasCookie(cs, "cf_clearance", "secret-value") || hits.Load() == 0 {
		t.Fatalf("после проверки: cookie %v, %v, заходов на страницу %d", cs, err, hits.Load())
	}
	if time.Since(start) < time.Second {
		t.Fatal("проверку не дождались: страница «Just a moment...» держится секунду")
	}
}

func TestPassTimesOut(t *testing.T) {
	needEdge(t)
	var hits atomic.Int32
	s := site(t, &hits)
	start := time.Now()
	_, err := New(Options{ProfileDir: profileDir(t), Timeout: 2 * time.Second}).Pass(context.Background(), s.URL+"/endless")
	if !errors.Is(err, ErrNotPassed) || !strings.Contains(err.Error(), "Один момент") {
		t.Fatalf("ожидалась ErrNotPassed с заголовком страницы, получено %v", err)
	}
	if d := time.Since(start); d > 30*time.Second {
		t.Fatalf("ожидание затянулось: %v", d)
	}
}

// Два одновременных вызова не открывают один профиль дважды: второй ждёт первого.
func TestPassCallsAreSerialized(t *testing.T) {
	needEdge(t)
	var hits atomic.Int32
	s := site(t, &hits)
	f := New(Options{ProfileDir: profileDir(t)})
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range 2 {
		wg.Go(func() { _, errs[i] = f.Pass(context.Background(), s.URL+"/page") })
	}
	wg.Wait()
	if errs[0] != nil || errs[1] != nil {
		t.Fatalf("ошибки: %v, %v", errs[0], errs[1])
	}
}

// В журнал — только число cookie, не значения.
func TestPassLogsNoCookieValues(t *testing.T) {
	needEdge(t)
	var hits atomic.Int32
	s := site(t, &hits)
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	if _, err := New(Options{ProfileDir: profileDir(t), Log: log}).Pass(context.Background(), s.URL+"/page"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "secret-value") {
		t.Fatalf("значение cookie в журнале: %s", buf.String())
	}
}

func TestPassRejectsProxyWithPassword(t *testing.T) {
	f := New(Options{ProfileDir: t.TempDir(), ExecPath: "msedge.exe", UserAgent: "UA", Proxy: "socks5://user:secret@127.0.0.1:1080"})
	_, err := f.Pass(context.Background(), "https://rutracker.org/forum/index.php")
	if !errors.Is(err, ErrProxyAuth) || strings.Contains(err.Error(), "secret") {
		t.Fatalf("ожидалась ErrProxyAuth без пароля в тексте, получено %v", err)
	}
}

func TestPassNeedsProfileDir(t *testing.T) {
	if _, err := New(Options{ExecPath: "msedge.exe", UserAgent: "UA"}).Pass(context.Background(), "https://rutracker.org/"); err == nil {
		t.Fatal("ошибки нет")
	}
}

func TestSiteCookies(t *testing.T) {
	all := []*network.Cookie{
		{Name: "a", Value: "1", Domain: ".rutracker.org", Path: "/"},
		{Name: "b", Value: "2", Domain: "rutracker.org", Path: "/forum/"},
		{Name: "c", Value: "3", Domain: "other.org", Path: "/"},
		{Name: "d", Value: "4", Domain: ".rutracker.org", Path: "/", Expires: 1893456000, HTTPOnly: true, Secure: true},
	}
	got := siteCookies(all, "rutracker.org")
	if len(got) != 3 || got[2].Expires.Unix() != 1893456000 || !got[2].HttpOnly {
		t.Fatalf("cookie сайта: %+v", got)
	}
	if len(siteCookies(all, "forum.rutracker.org")) != 2 {
		t.Fatal("поддомен получает cookie домена .rutracker.org, но не cookie только хоста rutracker.org")
	}
}
