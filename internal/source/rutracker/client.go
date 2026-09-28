// Package rutracker — источник раздач Rutracker (спека, раздел 6). Разделы, топы и свежие цифры
// — через официальный API: без входа и без Cloudflare. Названия, описания, постеры и поиск —
// со страниц форума: им нужен пропуск Cloudflare (его добывает скрытый Edge), поиску — ещё и вход.
package rutracker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/time/rate"

	"kinodom/internal/netx"
)

// Name — имя источника в каталоге; title — в текстах ошибок.
const (
	Name  = "rutracker"
	title = "Rutracker"
)

// Встроенные адреса (спека, разделы 5 и 6).
var (
	DefaultMirrors  = []string{"https://rutracker.org", "https://rutracker.net"}
	DefaultAPIBase  = "https://api.rutracker.cc"
	DefaultFeedBase = "https://feed.rutracker.cc"
)

// Passer добывает пропуск Cloudflare — cookie сайта после прохода проверки (edge.Fetcher).
type Passer interface {
	Pass(ctx context.Context, pageURL string) ([]*http.Cookie, error)
}

type Options struct {
	Proxy           string        // прокси для трекеров из настроек; пусто — напрямую
	Mirrors         []string      // пусто — DefaultMirrors
	APIBase         string        // пусто — DefaultAPIBase
	FeedBase        string        // пусто — DefaultFeedBase
	UserAgent       string        // UA Edge (edge.UserAgent): пропуск Cloudflare привязан к нему
	Passer          Passer        // nil — пропуск не добыть: работает только API
	Login, Password string        // пусто — без входа: поиск недоступен
	Rate            rate.Limit    // 0 — 1 запрос/с на весь Rutracker (тесты ускоряют)
	Timeout         time.Duration // 0 — 90 с
	Log             *slog.Logger  // nil — без журнала
}

type Rutracker struct {
	forum    *netx.Client // сайт: зеркала, cookie, признаки ответа форума
	api      *netx.Client // api.rutracker.cc и лента feed.rutracker.cc
	feedBase string
	jar      http.CookieJar
	passer   Passer
	passes   singleflight.Group
	log      *slog.Logger

	mu              sync.Mutex
	login, password string
	loginBlock      error // неверный пароль или капча: автоматический вход не повторяется
	tree            *forumTree
	treeAt          time.Time
}

func New(o Options) (*Rutracker, error) {
	mirrors := o.Mirrors
	if len(mirrors) == 0 {
		mirrors = DefaultMirrors
	}
	mirrors = slices.Clone(mirrors) // список по умолчанию — общий, его правка не должна менять источник
	if o.APIBase == "" {
		o.APIBase = DefaultAPIBase
	}
	if o.FeedBase == "" {
		o.FeedBase = DefaultFeedBase
	}
	if o.Rate == 0 {
		o.Rate = 1
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	feed, err := url.Parse(o.FeedBase)
	if err != nil || feed.Host == "" {
		return nil, fmt.Errorf("Rutracker: адрес ленты %q — не адрес сайта", o.FeedBase)
	}
	jar, _ := cookiejar.New(nil)
	lim := rate.NewLimiter(o.Rate, 1) // 1 запрос/с на весь Rutracker: форум, API и лента вместе
	forum, err := netx.NewClient(netx.Options{
		Name: title, Mirrors: mirrors, Proxy: o.Proxy, UserAgent: o.UserAgent,
		Classify: classify, Jar: jar, Limiter: lim, Timeout: o.Timeout, Log: o.Log,
	})
	if err != nil {
		return nil, err
	}
	api, err := netx.NewClient(netx.Options{
		Name: title + " API", Mirrors: []string{o.APIBase}, ExtraHosts: []string{feed.Host},
		Proxy: o.Proxy, Limiter: lim, Timeout: o.Timeout, Log: o.Log,
	})
	if err != nil {
		return nil, err
	}
	return &Rutracker{forum: forum, api: api, feedBase: strings.TrimRight(o.FeedBase, "/"), jar: jar,
		passer: o.Passer, log: o.Log, login: o.Login, password: o.Password}, nil
}

func (r *Rutracker) Name() string { return Name }

// Mirror — зеркало форума, ответившее последним.
func (r *Rutracker) Mirror() string { return r.forum.Mirror() }

// forumPage — страница форума (путь от корня зеркала: /forum/…); form != "" — POST формы.
// На проверке Cloudflare добывает пропуск и повторяет запрос один раз. Тело — уже в UTF-8.
func (r *Rutracker) forumPage(ctx context.Context, path, form string, opts ...netx.GetOption) (*netx.Page, error) {
	do := func() (*netx.Page, error) {
		if form != "" {
			return r.forum.Post(ctx, path, form, opts...)
		}
		return r.forum.Get(ctx, path, opts...)
	}
	p, err := do()
	if errors.Is(err, netx.ErrChallenge) {
		if err = r.renewPass(ctx, path); err == nil {
			p, err = do()
		}
	}
	if err != nil {
		return nil, err
	}
	if p.Status != http.StatusOK {
		return nil, fmt.Errorf("Rutracker: %s — ответ %d", p.URL.Path, p.Status)
	}
	if p.Body, err = decode(p.Body); err != nil {
		return nil, fmt.Errorf("Rutracker: страница %s не читается в windows-1251: %w", p.URL.Path, err)
	}
	return p, nil
}

// renewPass добывает новый пропуск Cloudflare на текущем зеркале. Одновременные запросы,
// попавшие на проверку, ждут одной добычи (Edge — тяжёлый: 200–600 МБ на время прохода).
func (r *Rutracker) renewPass(ctx context.Context, path string) error {
	if r.passer == nil {
		return fmt.Errorf("Rutracker: форум закрыт проверкой Cloudflare, а Edge не подключён: %w", netx.ErrChallenge)
	}
	mirror := r.forum.Mirror()
	// Добыча идёт на своём контексте: отмена одного из ждущих (закрыли вкладку поиска) не должна
	// сорвать её остальным. Сам Edge ограничен своим таймаутом (45 с + 30 с).
	passCtx := context.WithoutCancel(ctx)
	ch := r.passes.DoChan(mirror, func() (any, error) {
		cookies, err := r.passer.Pass(passCtx, mirror+path)
		if err != nil {
			return nil, err
		}
		u, _ := url.Parse(mirror + "/")
		r.jar.SetCookies(u, cookies)
		return nil, nil
	})
	select {
	case res := <-ch:
		if res.Err != nil {
			return fmt.Errorf("Rutracker: не удаётся пройти защиту Cloudflare: %w", &passError{res.Err})
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// passError — пропуск не добыт. Для errors.Is это netx.ErrChallenge: форум закрыт проверкой.
type passError struct{ err error }

func (e *passError) Error() string        { return e.err.Error() }
func (e *passError) Unwrap() error        { return e.err }
func (e *passError) Is(target error) bool { return target == netx.ErrChallenge }

// decode — страницы форума в windows-1251 (спека, раздел 6); разбор работает с UTF-8.
func decode(b []byte) ([]byte, error) { return charmap.Windows1251.NewDecoder().Bytes(b) }

// cp1251 — строка для запроса к форуму: поиск nm и поля входа форум читает в windows-1251.
func cp1251(s string) string {
	out, _ := encoding.ReplaceUnsupported(charmap.Windows1251.NewEncoder()).String(s)
	return out
}

var topicNotFound = []byte(cp1251("Тема не найдена"))

// classify — признаки форума поверх общих (спека, раздел 5; образцы — исследование, раздел 9).
// Тело ещё в windows-1251: латинские метки в нём те же байты, русский текст — перекодирован.
func classify(p *netx.Page) netx.Verdict {
	b := p.Body
	switch {
	case strings.HasSuffix(p.URL.Path, "/login.php") && bytes.Contains(b, []byte(`id="login-form-full"`)):
		return netx.LoginRequired
	case bytes.Contains(b, topicNotFound) && !bytes.Contains(b, []byte(`id="topic-title"`)):
		return netx.Removed
	case p.Status == http.StatusOK && strings.Contains(p.Header.Get("Content-Type"), "text/html") &&
		!bytes.Contains(b, []byte(`id="page_container"`)):
		return netx.MirrorDown
	}
	return netx.OK
}
