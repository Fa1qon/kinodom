package edge

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/storage"
	"github.com/chromedp/chromedp"

	"kinodom/internal/netx"
)

var (
	ErrNotPassed = errors.New("Edge не прошёл проверку Cloudflare")
	ErrProxyAuth = errors.New("Edge не умеет прокси с логином и паролем — пропуск Cloudflare добыть нельзя")
)

type Options struct {
	ProfileDir string        // постоянный профиль (data\edge-profile): повторный проход ~1 с вместо 16–20 с
	Proxy      string        // прокси для трекеров из настроек; с логином и паролем Edge не умеет
	ExecPath   string        // "" — установленный Edge (ExecPath)
	UserAgent  string        // "" — UserAgent()
	Timeout    time.Duration // на проход проверки; 0 — 45 с
	Log        *slog.Logger  // nil — без журнала
}

// Fetcher добывает пропуск Cloudflare скрытым Edge. Безопасен для одновременных вызовов:
// Edge открывается по одному (профиль нельзя открыть дважды).
type Fetcher struct {
	o  Options
	mu sync.Mutex
}

func New(o Options) *Fetcher {
	if o.Timeout == 0 {
		o.Timeout = 45 * time.Second
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	return &Fetcher{o: o}
}

// Pass открывает pageURL в скрытом Edge, ждёт ухода со страницы проверки Cloudflare и
// возвращает cookie сайта (среди них cf_clearance). Второй одновременный вызов ждёт первого
// и, скорее всего, пройдёт за ~1 с: профиль уже с пропуском.
func (f *Fetcher) Pass(ctx context.Context, pageURL string) ([]*http.Cookie, error) {
	u, err := url.Parse(pageURL)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("Edge: %q — не адрес страницы", pageURL)
	}
	opts, err := f.options()
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	start := time.Now()
	ctx, cancel := context.WithTimeout(ctx, f.o.Timeout+30*time.Second) // запуск и закрытие — сверх ожидания прохода
	defer cancel()
	alloc, cancelAlloc := chromedp.NewExecAllocator(ctx, opts...)
	defer cancelAlloc() // ждёт выхода процесса Edge
	tab, cancelTab := chromedp.NewContext(alloc)
	defer cancelTab()
	err = chromedp.Run(tab, chromedp.ActionFunc(func(c context.Context) error {
		_, _, errText, _, err := page.Navigate(pageURL).Do(c)
		if err == nil && errText != "" {
			err = errors.New(errText)
		}
		return err
	}))
	if err != nil {
		return nil, fmt.Errorf("Edge: страница %s не открылась: %w", u.Host, err)
	}
	if err := f.waitPassed(tab); err != nil {
		return nil, err
	}
	var all []*network.Cookie
	err = chromedp.Run(tab, chromedp.ActionFunc(func(c context.Context) error {
		var err error
		all, err = storage.GetCookies().Do(cdp.WithExecutor(c, chromedp.FromContext(c).Browser))
		return err
	}))
	if err != nil {
		return nil, fmt.Errorf("Edge: cookie не прочитались: %w", err)
	}
	// Штатное закрытие: cookie должны успеть записаться в профиль, тогда следующий проход ~1 с.
	if err := chromedp.Cancel(tab); err != nil {
		f.o.Log.Warn("Edge закрылся не штатно", "err", err)
	}
	cookies := siteCookies(all, u.Hostname())
	f.o.Log.Info("Edge: пропуск Cloudflare получен", "host", u.Host, "sec", time.Since(start).Round(100*time.Millisecond).Seconds(), "cookies", len(cookies))
	return cookies, nil
}

// options — флаги варианта «a» из исследования (раздел 1). Флаги chromedp по умолчанию не берём:
// с ними (--enable-automation, HeadlessChrome в UA) Cloudflare показывает галочку
// «Подтвердите, что вы человек».
func (f *Fetcher) options() ([]chromedp.ExecAllocatorOption, error) {
	if f.o.ProfileDir == "" {
		return nil, errors.New("Edge: не задана папка профиля")
	}
	exec := f.o.ExecPath
	if exec == "" {
		exec = ExecPath()
	}
	if exec == "" {
		return nil, ErrNoEdge
	}
	ua := f.o.UserAgent
	if ua == "" {
		var err error
		if ua, err = UserAgent(); err != nil {
			return nil, err
		}
	}
	opts := []chromedp.ExecAllocatorOption{
		chromedp.ExecPath(exec),
		chromedp.UserDataDir(f.o.ProfileDir),
		chromedp.Flag("headless", "new"),
		chromedp.Flag("disable-blink-features", "AutomationControlled"),
		chromedp.Flag("user-agent", ua),
		chromedp.Flag("window-size", "1920,1080"),
		chromedp.Flag("screen-info", "{0,0 1920x1080}"), // без него в headless экран 800×600
		chromedp.Flag("lang", "ru-RU"),
		chromedp.Flag("accept-lang", "ru-RU,ru"),
		chromedp.Flag("no-first-run", true),
		chromedp.Flag("no-default-browser-check", true),
		chromedp.Flag("disable-sync", true),
	}
	if f.o.Proxy != "" {
		p, err := netx.ParseProxy(f.o.Proxy)
		if err != nil {
			return nil, err
		}
		if p.User != nil {
			return nil, ErrProxyAuth
		}
		opts = append(opts, chromedp.ProxyServer(p.Scheme+"://"+p.Host))
	}
	return opts, nil
}

// waitPassed ждёт, пока вкладка уйдёт со страницы проверки. Заголовок читается из списка
// вкладок, без выполнения JS на странице: проверка может заметить управление через CDP.
func (f *Fetcher) waitPassed(tab context.Context) error {
	deadline := time.Now().Add(f.o.Timeout)
	var title string
	for time.Now().Before(deadline) {
		t, addr, err := tabInfo(tab)
		if err != nil {
			return fmt.Errorf("Edge: %w", err)
		}
		title = t
		// Пока страница грузится, заголовок вкладки — её адрес без схемы; about:… — ещё пусто.
		loading := title == "" || strings.HasPrefix(title, "about:") || title == addr ||
			title == strings.TrimPrefix(strings.TrimPrefix(addr, "https://"), "http://")
		if !loading && !isChallengeTitle(title) {
			time.Sleep(time.Second) // скрипт страницы дописывает cookie
			return nil
		}
		select {
		case <-tab.Done():
			return fmt.Errorf("Edge: %w", tab.Err())
		case <-time.After(250 * time.Millisecond):
		}
	}
	return fmt.Errorf("%w за %s (заголовок страницы: %q)", ErrNotPassed, f.o.Timeout, title)
}

func tabInfo(tab context.Context) (title, addr string, err error) {
	infos, err := chromedp.Targets(tab)
	if err != nil {
		return "", "", err
	}
	id := chromedp.FromContext(tab).Target.TargetID
	for _, i := range infos {
		if i.TargetID == id {
			return i.Title, i.URL, nil
		}
	}
	return "", "", errors.New("вкладка Edge пропала")
}

// isChallengeTitle — страница проверки Cloudflare: «Just a moment…» или «Один момент…»
// (заголовок переведён на язык браузера).
func isChallengeTitle(t string) bool {
	t = strings.ToLower(t)
	return strings.Contains(t, "just a moment") || strings.Contains(t, "один момент") || strings.Contains(t, "attention required")
}

// siteCookies — cookie, которые браузер отправил бы на host: «.rutracker.org» — этому домену
// и поддоменам, «rutracker.org» без точки — только этому хосту.
func siteCookies(all []*network.Cookie, host string) []*http.Cookie {
	var out []*http.Cookie
	for _, c := range all {
		if d, ok := strings.CutPrefix(c.Domain, "."); ok {
			if host != d && !strings.HasSuffix(host, "."+d) {
				continue
			}
		} else if host != c.Domain {
			continue
		}
		hc := &http.Cookie{Name: c.Name, Value: c.Value, Domain: c.Domain, Path: c.Path, Secure: c.Secure, HttpOnly: c.HTTPOnly}
		if c.Expires > 0 {
			hc.Expires = time.Unix(int64(c.Expires), 0)
		}
		out = append(out, hc)
	}
	return out
}
