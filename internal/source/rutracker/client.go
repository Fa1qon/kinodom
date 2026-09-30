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
	"kinodom/internal/source"
)

// Name — имя источника в каталоге; title — в текстах ошибок.
const (
	Name  = "rutracker"
	title = "Rutracker"
)

// Passer добывает пропуск Cloudflare (edge.Fetcher): cookie сайта после прохода проверки и
// User-Agent, с которым браузер её прошёл. Пропуск привязан к UA; после обновления Edge UA новый.
type Passer interface {
	Pass(ctx context.Context, pageURL string) (cookies []*http.Cookie, userAgent string, err error)
}

// Адресов Rutracker в программе нет: адрес сайта вводит пользователь, адреса API и ленты — по
// правилу из него (спека этапа 11a, раздел 6). Без адреса источник выключен.
type Options struct {
	Proxy           *netx.Proxy   // прокси для трекеров из настроек; nil — напрямую
	Mirrors         []string      // адреса сайта; пусто — адрес не введён, источник выключен
	APIBase         string        // пусто — по правилу из первого адреса сайта
	FeedBase        string        // пусто — по правилу из первого адреса сайта
	UserAgent       string        // UA Edge (edge.UserAgent): пропуск Cloudflare привязан к нему
	Passer          Passer        // nil — пропуск не добыть: работает только API
	Login, Password string        // пусто — без входа: поиск недоступен
	Rate            rate.Limit    // 0 — 1 запрос/с на весь Rutracker (тесты ускоряют)
	Timeout         time.Duration // 0 — 90 с
	Log             *slog.Logger  // nil — без журнала
	// OnLogin — состояние входа изменилось (приложение ставит и снимает проблему rutracker.login).
	// Вызывается без блокировок источника; nil — не сообщать.
	OnLogin func(LoginInfo)
}

type Rutracker struct {
	forum    *netx.Client // сайт: зеркала, cookie, признаки ответа форума
	api      *netx.Client // API и лента
	jar      http.CookieJar
	onLogin  func(LoginInfo)
	passer   Passer
	passes   singleflight.Group
	sessions SessionStore // nil — сессия не сохраняется (под mu)
	log      *slog.Logger
	now      func() time.Time // часы (тесты подменяют)

	loginMu sync.Mutex // один вход за раз: одновременные запросы ждут его, а не входят сами

	addrMu   sync.Mutex
	feedBase string   // адрес ленты без «/» на конце
	mirrors  []string // все зеркала: при смене учётной записи сессия сбрасывается на каждом

	mu              sync.Mutex
	login, password string
	credGen         int                    // растёт при SetCredentials: неудача старого входа не блокирует новый пароль
	loginBlock      error                  // неверный пароль или капча: автоматический вход не повторяется
	passFail        map[string]passFailure // зеркало → последняя неудачная добыча пропуска
	tree            *forumTree
	treeAt          time.Time
	loginRetryAt    time.Time // до этого времени страница раздачи не входит сама (после временной неудачи)
	loginRetryErr   error     // временная неудача входа, из-за которой стоит пауза
	loginWarned     string    // неудача входа, о которой журнал уже знает
	treeRetryAt     time.Time // до этого времени не пробовать снова обновить дерево разделов
	loginSeen       LoginInfo // о каком состоянии входа уже сообщили OnLogin
}

func New(o Options) (*Rutracker, error) {
	if o.Rate == 0 {
		o.Rate = 1
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	jar, _ := cookiejar.New(nil)
	lim := rate.NewLimiter(o.Rate, 1) // 1 запрос/с на весь Rutracker: форум, API и лента вместе
	forum, err := netx.NewClient(netx.Options{
		Name: title, Proxy: o.Proxy, UserAgent: o.UserAgent,
		Classify: classify, Jar: jar, Limiter: lim, Timeout: o.Timeout, Log: o.Log,
	})
	if err != nil {
		return nil, err
	}
	api, err := netx.NewClient(netx.Options{
		Name: title + " API", Proxy: o.Proxy, Limiter: lim, Timeout: o.Timeout, Log: o.Log,
	})
	if err != nil {
		return nil, err
	}
	r := &Rutracker{forum: forum, api: api, jar: jar,
		onLogin: o.OnLogin, passer: o.Passer, log: o.Log, now: time.Now, login: o.Login, password: o.Password}
	if err := r.setAddresses(o.Mirrors, o.APIBase, o.FeedBase); err != nil {
		return nil, err
	}
	return r, nil
}

// SetAddresses — адрес сайта и служебные адреса из настроек; api и feed "" — по правилу из адреса
// сайта, site "" — источник выключен. Действует со следующего запроса; пауза входа после временной
// неудачи и неудачные добычи пропуска забываются — новый адрес пробуется сразу.
func (r *Rutracker) SetAddresses(site, api, feed string) error {
	var mirrors []string
	if site != "" {
		mirrors = []string{site}
	}
	if err := r.setAddresses(mirrors, api, feed); err != nil {
		return err
	}
	r.mu.Lock()
	r.loginRetryAt, r.loginRetryErr, r.passFail = time.Time{}, nil, nil
	r.mu.Unlock()
	r.noteLoginState()
	return nil
}

func (r *Rutracker) setAddresses(mirrors []string, api, feed string) error {
	mirrors = slices.Clone(mirrors)
	for i := range mirrors {
		mirrors[i] = strings.TrimRight(mirrors[i], "/")
	}
	if len(mirrors) == 0 {
		api, feed = "", ""
	} else if api == "" || feed == "" {
		ruleAPI, ruleFeed := source.RutrackerService(mirrors[0])
		if api == "" {
			api = ruleAPI
		}
		if feed == "" {
			feed = ruleFeed
		}
	}
	var apiMirrors, extra []string
	if api != "" {
		fu, err := url.Parse(feed)
		if err != nil || fu.Host == "" {
			return fmt.Errorf("Rutracker: адрес ленты %q — не адрес сайта", feed)
		}
		apiMirrors, extra = []string{api}, []string{fu.Host}
	}
	if err := r.forum.SetMirrors(mirrors); err != nil {
		return err
	}
	if err := r.api.SetMirrors(apiMirrors, extra...); err != nil {
		return err
	}
	r.addrMu.Lock()
	r.mirrors, r.feedBase = mirrors, strings.TrimRight(feed, "/")
	r.addrMu.Unlock()
	return nil
}

// Configured — адрес Rutracker введён.
func (r *Rutracker) Configured() bool { return r.forum.Configured() }

// Check — «Проверить» в мастере начальных настроек: главная форума открывается (с пропуском
// Cloudflare, если он нужен) и похожа на Rutracker. Вход проверяет Relogin.
func (r *Rutracker) Check(ctx context.Context) error {
	if err := r.notConfigured(); err != nil {
		return err
	}
	_, err := r.forumPage(ctx, "/forum/index.php", "")
	return err
}

// notConfigured — ошибка «адрес не введён» с именем трекера; nil — адрес есть.
func (r *Rutracker) notConfigured() error {
	if r.Configured() {
		return nil
	}
	return fmt.Errorf("%s: %w", title, source.ErrNotConfigured)
}

// feed — адрес ленты; "" — адрес не введён.
func (r *Rutracker) feed() string {
	r.addrMu.Lock()
	defer r.addrMu.Unlock()
	return r.feedBase
}

func (r *Rutracker) Name() string { return Name }

// Mirror — зеркало форума, ответившее последним.
func (r *Rutracker) Mirror() string { return r.forum.Mirror() }

// TopicURL — страница раздачи на текущем зеркале (ссылка «На трекере» в пульте); "" — адрес не введён.
func (r *Rutracker) TopicURL(id string) string {
	m := r.forum.Mirror()
	if m == "" {
		return ""
	}
	return m + "/forum/viewtopic.php?t=" + id
}

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
	// Недавно не вышло — не запускаем Edge снова: каталог иначе гонял бы его (45–75 с и
	// 200–600 МБ) на каждую раздачу. Через passRetryAfter — новая попытка.
	r.mu.Lock()
	last, failed := r.passFail[mirror]
	r.mu.Unlock()
	if failed && r.now().Sub(last.at) < passRetryAfter {
		return fmt.Errorf("Rutracker: не удаётся пройти защиту Cloudflare: %w", &passError{last.err})
	}
	// Добыча идёт на своём контексте: отмена одного из ждущих (закрыли вкладку поиска) не должна
	// сорвать её остальным. Сам Edge ограничен своим таймаутом (45 с + 30 с).
	passCtx := context.WithoutCancel(ctx)
	ch := r.passes.DoChan(mirror, func() (any, error) {
		cookies, ua, err := r.passer.Pass(passCtx, mirror+path)
		r.mu.Lock()
		if err != nil {
			if r.passFail == nil {
				r.passFail = map[string]passFailure{}
			}
			r.passFail[mirror] = passFailure{at: r.now(), err: err}
		} else {
			delete(r.passFail, mirror)
		}
		r.mu.Unlock()
		if err != nil {
			return nil, err
		}
		if ua != "" {
			r.forum.SetUserAgent(ua) // пропуск привязан к UA браузера; после обновления Edge он новый
		}
		u, _ := url.Parse(mirror + "/")
		r.jar.SetCookies(u, cookies)
		r.saveSession(passCtx)
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

// passRetryAfter — сколько после неудачной добычи пропуска не запускать Edge снова (как шаг
// повторов каталога, спека, раздел 7).
const passRetryAfter = 10 * time.Minute

type passFailure struct {
	at  time.Time
	err error
}

// passError — пропуск не добыт. Для errors.Is это netx.ErrChallenge: форум закрыт проверкой.
type passError struct{ err error }

func (e *passError) Error() string        { return e.err.Error() }
func (e *passError) Unwrap() error        { return e.err }
func (e *passError) Is(target error) bool { return target == netx.ErrChallenge }

// decode — страницы форума в windows-1251 (спека, раздел 6); разбор работает с UTF-8.
func decode(b []byte) ([]byte, error) { return charmap.Windows1251.NewDecoder().Bytes(b) }

// cp1251 — строка для запроса к форуму: поиск nm и поля входа форум читает в windows-1251.
// Символы, которых в windows-1251 нет, уходят как у браузера — «&#233;».
func cp1251(s string) string {
	out, _ := encoding.HTMLEscapeUnsupported(charmap.Windows1251.NewEncoder()).String(s)
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
	case strings.HasSuffix(p.URL.Path, "/viewtopic.php") && bytes.Contains(b, topicNotFound) &&
		!bytes.Contains(b, []byte(`id="topic-title"`)):
		return netx.Removed
	case p.Status == http.StatusOK && strings.Contains(p.Header.Get("Content-Type"), "text/html") &&
		!bytes.Contains(b, []byte(`id="page_container"`)):
		return netx.MirrorDown
	}
	return netx.OK
}
