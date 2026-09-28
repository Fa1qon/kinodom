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
	"os/exec"
	"strconv"
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
// «Just a moment...», через секунду сама переходит на /page; /plain — обычная страница без
// cookie; остальное — проверка без конца.
func site(t *testing.T, pageHits *atomic.Int32) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch r.URL.Path {
		case "/page":
			pageHits.Add(1)
			http.SetCookie(w, &http.Cookie{Name: "cf_clearance", Value: "secret-value", Path: "/"})
			fmt.Fprint(w, `<html><head><title>Раздача / Release</title></head><body>ok</body></html>`)
		case "/plain":
			fmt.Fprint(w, `<html><head><title>Главная</title></head><body>ok</body></html>`)
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
	cs, _, err := New(Options{ProfileDir: profileDir(t)}).Pass(context.Background(), s.URL+"/page")
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
	cs, _, err := New(Options{ProfileDir: profileDir(t)}).Pass(context.Background(), s.URL+"/challenge")
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
	_, _, err := New(Options{ProfileDir: profileDir(t), Timeout: 2 * time.Second}).Pass(context.Background(), s.URL+"/endless")
	if !errors.Is(err, ErrNotPassed) || !strings.Contains(err.Error(), "Один момент") {
		t.Fatalf("ожидалась ErrNotPassed с заголовком страницы, получено %v", err)
	}
	if d := time.Since(start); d > 30*time.Second {
		t.Fatalf("ожидание затянулось: %v", d)
	}
}

// Страница открылась, а пропуска нет: без cf_clearance форум снова ответит проверкой, поэтому
// это неудача (её запомнят и не будут гонять Edge на каждый запрос), а не успех.
func TestPassWithoutClearanceIsNotPassed(t *testing.T) {
	needEdge(t)
	var hits atomic.Int32
	s := site(t, &hits)
	_, _, err := New(Options{ProfileDir: profileDir(t)}).Pass(context.Background(), s.URL+"/plain")
	if !errors.Is(err, ErrNotPassed) || !strings.Contains(err.Error(), "Главная") {
		t.Fatalf("ожидалась ErrNotPassed с заголовком страницы, получено %v", err)
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
		wg.Go(func() { _, _, errs[i] = f.Pass(context.Background(), s.URL+"/page") })
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
	if _, _, err := New(Options{ProfileDir: profileDir(t), Log: log}).Pass(context.Background(), s.URL+"/page"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "secret-value") {
		t.Fatalf("значение cookie в журнале: %s", buf.String())
	}
}

func TestPassRejectsProxyWithPassword(t *testing.T) {
	f := New(Options{ProfileDir: t.TempDir(), ExecPath: "msedge.exe", UserAgent: "UA", Proxy: "socks5://user:secret@127.0.0.1:1080"})
	_, _, err := f.Pass(context.Background(), "https://rutracker.org/forum/index.php")
	if !errors.Is(err, ErrProxyAuth) || strings.Contains(err.Error(), "secret") {
		t.Fatalf("ожидалась ErrProxyAuth без пароля в тексте, получено %v", err)
	}
}

func TestPassNeedsProfileDir(t *testing.T) {
	if _, _, err := New(Options{ExecPath: "msedge.exe", UserAgent: "UA"}).Pass(context.Background(), "https://rutracker.org/"); err == nil {
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

// Профиль держит другой Edge (остался от прошлого запуска или открыт командой source с тем же
// --profile) — понятная ошибка сразу, а не «chrome failed to start:» (ревью этапа 4).
func TestPassReportsBusyProfile(t *testing.T) {
	needEdge(t)
	dir := profileDir(t)
	other := exec.Command(ExecPath(), "--headless=new", "--user-data-dir="+dir, "--no-first-run", "about:blank")
	if err := other.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		exec.Command("taskkill", "/F", "/T", "/PID", strconv.Itoa(other.Process.Pid)).Run()
		other.Wait()
	})
	for deadline := time.Now().Add(15 * time.Second); !profileBusy(dir); time.Sleep(100 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("чужой Edge не занял профиль за 15 с")
		}
	}
	var hits atomic.Int32
	s := site(t, &hits)
	start := time.Now()
	_, _, err := New(Options{ProfileDir: dir}).Pass(context.Background(), s.URL+"/page")
	if !errors.Is(err, ErrProfileBusy) || time.Since(start) > 5*time.Second {
		t.Fatalf("ожидалась ErrProfileBusy сразу, получено %v за %v", err, time.Since(start))
	}
	// Совет — перезапуск службы (не «снять msedge.exe»: у пользователя свой Edge), и какая папка занята.
	if !strings.Contains(err.Error(), "перезапустите службу") || !strings.Contains(err.Error(), dir) {
		t.Fatalf("текст %q", err)
	}
}

// Сообщения chromedp уходят в журнал Kinodom (Debug), а не в стандартный log — мимо файлов
// журнала службы. В обычном проходе chromedp молчит, поэтому проверяется сам переходник.
func TestChromedpMessagesGoToJournal(t *testing.T) {
	var buf bytes.Buffer
	f := New(Options{Log: slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))})
	f.chromedpLogf("could not unmarshal event: %v", "Page.newEvent")
	if !strings.Contains(buf.String(), "level=DEBUG") || !strings.Contains(buf.String(), "could not unmarshal event: Page.newEvent") {
		t.Fatalf("журнал:\n%s", buf.String())
	}
}

// Модуль edge: привязывает дочерние процессы при старте, работает до отмены (спека, раздел 3).
func TestModuleRunsUntilCancelled(t *testing.T) {
	m := NewModule(nil)
	if m.Name() != "edge" {
		t.Fatal(m.Name())
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- m.Run(ctx) }()
	select {
	case err := <-done:
		t.Fatalf("модуль завершился сам: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
