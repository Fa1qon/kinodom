package rutracker

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"kinodom/internal/netx"
	"kinodom/internal/source/rutracker/rutrackertest"
)

var ctx = context.Background()

// fakePasser — «Edge»: считает вызовы и выдаёт пропуск (или ошибку).
type fakePasser struct {
	calls atomic.Int32
	delay time.Duration
	err   error
}

func (f *fakePasser) Pass(ctx context.Context, _ string) ([]*http.Cookie, error) {
	f.calls.Add(1)
	select {
	case <-time.After(f.delay):
	case <-ctx.Done(): // как настоящий Edge: отмена прерывает добычу
		return nil, ctx.Err()
	}
	if f.err != nil {
		return nil, f.err
	}
	return []*http.Cookie{rutrackertest.PassCookie()}, nil
}

func newRutracker(t *testing.T, s *rutrackertest.Server, mod func(*Options)) *Rutracker {
	t.Helper()
	o := Options{Mirrors: []string{s.Forum.URL}, APIBase: s.API.URL, FeedBase: s.Feed.URL, Rate: 1000, Passer: &fakePasser{}}
	if mod != nil {
		mod(&o)
	}
	r, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestChallengeTriggersPassAndRetries(t *testing.T) {
	s := rutrackertest.NewServer(t)
	s.NeedPass = true
	p := &fakePasser{}
	r := newRutracker(t, s, func(o *Options) { o.Passer = p })
	pg, err := r.forumPage(ctx, "/forum/viewtopic.php?t=6914565", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(pg.Body), "Экстрасенсы") {
		t.Fatal("страница не раскодирована из windows-1251")
	}
	if p.calls.Load() != 1 || s.Hits("/forum/viewtopic.php") != 2 {
		t.Fatalf("добыч %d, запросов %d", p.calls.Load(), s.Hits("/forum/viewtopic.php"))
	}
	// Пропуск остался в cookie: следующий запрос — без добычи.
	r.forumPage(ctx, "/forum/viewtopic.php?t=6914565", "")
	if p.calls.Load() != 1 {
		t.Fatal("пропуск добыт повторно")
	}
}

func TestParallelChallengesShareOnePass(t *testing.T) {
	s := rutrackertest.NewServer(t)
	s.NeedPass = true
	p := &fakePasser{delay: 300 * time.Millisecond}
	r := newRutracker(t, s, func(o *Options) { o.Passer = p })
	var wg sync.WaitGroup
	for range 3 {
		wg.Go(func() {
			if _, err := r.forumPage(ctx, "/forum/viewtopic.php?t=6914565", ""); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if p.calls.Load() != 1 {
		t.Fatalf("добыч %d, нужна одна на все запросы", p.calls.Load())
	}
}

func TestPassFailureIsChallengeError(t *testing.T) {
	s := rutrackertest.NewServer(t)
	s.NeedPass = true
	r := newRutracker(t, s, func(o *Options) {
		o.Passer = &fakePasser{err: errors.New("Edge не прошёл проверку Cloudflare за 45 с")}
	})
	_, err := r.forumPage(ctx, "/forum/viewtopic.php?t=6914565", "")
	if !errors.Is(err, netx.ErrChallenge) || !strings.Contains(err.Error(), "защиту Cloudflare") {
		t.Fatalf("ожидалась понятная ошибка Cloudflare, получено %v", err)
	}
}

func TestNoPasserMeansChallengeError(t *testing.T) {
	s := rutrackertest.NewServer(t)
	s.NeedPass = true
	r := newRutracker(t, s, func(o *Options) { o.Passer = nil })
	if _, err := r.forumPage(ctx, "/forum/viewtopic.php?t=6914565", ""); !errors.Is(err, netx.ErrChallenge) {
		t.Fatalf("получено %v", err)
	}
}

func TestClassify(t *testing.T) {
	s := rutrackertest.NewServer(t)
	r := newRutracker(t, s, nil)
	if _, err := r.forumPage(ctx, "/forum/viewtopic.php?t=99999999", ""); !errors.Is(err, netx.ErrRemoved) {
		t.Errorf("«Тема не найдена» в windows-1251: %v", err)
	}
	if _, err := r.forumPage(ctx, "/forum/tracker.php?nm=x", ""); !errors.Is(err, netx.ErrLoginRequired) {
		t.Errorf("поиск гостем: %v", err)
	}
}

func TestParkedMirrorIsSkipped(t *testing.T) {
	s := rutrackertest.NewServer(t)
	parked := httptestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte("<html><body>Этот домен продаётся</body></html>"))
	})
	r := newRutracker(t, s, func(o *Options) { o.Mirrors = []string{parked, s.Forum.URL} })
	if _, err := r.forumPage(ctx, "/forum/viewtopic.php?t=6914565", ""); err != nil {
		t.Fatal(err)
	}
	if r.forum.Mirror() != s.Forum.URL {
		t.Fatalf("текущее зеркало %s", r.forum.Mirror())
	}
}

func httptestServer(t *testing.T, h http.HandlerFunc) string {
	t.Helper()
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	return s.URL
}

// Отмена одного из ждущих не срывает общую добычу остальным.
func TestCancelledCallerDoesNotSpoilSharedPass(t *testing.T) {
	s := rutrackertest.NewServer(t)
	s.NeedPass = true
	p := &fakePasser{delay: 300 * time.Millisecond}
	r := newRutracker(t, s, func(o *Options) { o.Passer = p })
	actx, cancel := context.WithCancel(ctx)
	errA := make(chan error, 1)
	go func() {
		_, err := r.forumPage(actx, "/forum/viewtopic.php?t=6914565", "")
		errA <- err
	}()
	time.Sleep(50 * time.Millisecond) // A — первый: добыча запущена его вызовом
	errB := make(chan error, 1)
	go func() {
		_, err := r.forumPage(ctx, "/forum/viewtopic.php?t=6914565", "")
		errB <- err
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	if err := <-errA; !errors.Is(err, context.Canceled) {
		t.Errorf("A: ожидалась отмена, получено %v", err)
	}
	if err := <-errB; err != nil {
		t.Fatalf("B: добыча сорвана чужой отменой: %v", err)
	}
	if p.calls.Load() != 1 {
		t.Fatalf("добыч %d", p.calls.Load())
	}
}

// Неудачная добыча запоминается: 10 минут форум не трогает Edge (каталог иначе запускал бы его
// на каждую раздачу), потом — новая попытка.
func TestFailedPassIsRememberedForAWhile(t *testing.T) {
	s := rutrackertest.NewServer(t)
	s.NeedPass = true
	s.Login, s.Password = "user", "pass"
	p := &fakePasser{err: errors.New("Edge не прошёл проверку Cloudflare")}
	r := newRutracker(t, s, func(o *Options) { o.Passer = p; o.Login, o.Password = "user", "pass" })
	base := time.Now()
	r.now = func() time.Time { return base }
	for range 3 {
		if _, err := r.Details(ctx, "6914565"); !errors.Is(err, netx.ErrChallenge) {
			t.Fatalf("получено %v", err)
		}
	}
	if p.calls.Load() != 1 {
		t.Fatalf("Edge запускали %d раз — после неудачи нужно подождать", p.calls.Load())
	}
	r.now = func() time.Time { return base.Add(11 * time.Minute) }
	r.Details(ctx, "6914565")
	if p.calls.Load() != 2 {
		t.Fatalf("через 11 минут Edge запускали %d раз, нужна новая попытка", p.calls.Load())
	}
}

// Символов, которых нет в windows-1251, форум ждёт как от браузера — «&#233;», а не байт 0x1A:
// иначе пароль с такими символами блокировал бы вход (ревью этапа 4).
func TestCP1251EscapesUnsupportedLikeBrowser(t *testing.T) {
	if got := cp1251("Amélie"); got != "Am&#233;lie" {
		t.Fatalf("cp1251: %q", got)
	}
}

// «Тема не найдена» в названии раздачи на странице поиска — не повод считать ответ «раздача
// удалена» (ревью этапа 4).
func TestTopicNotFoundOnlyOnTopicPage(t *testing.T) {
	body := []byte(`<div id="page_container">` + cp1251("Тема не найдена") + `</div>`)
	for path, want := range map[string]netx.Verdict{
		"/forum/viewtopic.php": netx.Removed,
		"/forum/tracker.php":   netx.OK,
	} {
		p := &netx.Page{URL: &url.URL{Scheme: "https", Host: "rutracker.org", Path: path}, Status: http.StatusOK,
			Header: http.Header{"Content-Type": {"text/html; charset=Windows-1251"}}, Body: body}
		if got := classify(p); got != want {
			t.Errorf("%s: вывод %d, нужно %d", path, got, want)
		}
	}
}
