package netx

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/time/rate"
)

// Verdict — вывод об ответе трекера (спека, раздел 5, «Классификация ответа трекера»).
// «Каркас есть, нужного блока нет» сюда не входит: это ошибка разбора страницы
// (source.ParseError), её возвращает парсер трекера, и зеркало при ней не меняется.
type Verdict int

const (
	OK            Verdict = iota
	MirrorDown            // зеркало недоступно — пробуем следующее, рабочее запоминаем
	Challenge             // проверка Cloudflare — нужен пропуск (Rutracker, этап 4)
	LoginRequired         // форма входа — нужен повторный вход (Rutracker, этап 4)
	Removed               // раздача удалена (Rutor: редирект на /d.php)
)

// ClassifyFunc — признаки конкретного трекера. Вызывается для ответов, прошедших общие
// проверки: свой домен, не проверка Cloudflare, не 5xx и не 451.
type ClassifyFunc func(p *Page) Verdict

var (
	ErrTrackerDown   = errors.New("трекер недоступен")
	ErrChallenge     = errors.New("трекер требует пройти проверку Cloudflare")
	ErrLoginRequired = errors.New("трекер требует войти")
	ErrRemoved       = errors.New("раздача удалена с трекера")
	ErrNotConfigured = errors.New("не указан адрес трекера")
	// ErrNotTracker — на всех зеркалах вместо трекера чужой сайт или заглушка; вместе с ErrTrackerDown.
	ErrNotTracker = errors.New("по адресу не сайт трекера")
)

// Page — ответ трекера целиком.
type Page struct {
	URL    *url.URL // итоговый адрес — после редиректов
	Status int
	Header http.Header
	Body   []byte
}

type Options struct {
	Name       string       // «Rutor» — для текстов ошибок и журнала
	Mirrors    []string     // базовые адреса зеркал по порядку предпочтения ("https://сайт"); пусто — адрес не введён
	ExtraHosts []string     // другие свои хосты (d.rutor.info): редирект туда — не «чужой сайт»
	Proxy      *Proxy       // прокси из настроек; nil — напрямую
	UserAgent  string       // пусто — User-Agent Go по умолчанию
	Classify   ClassifyFunc // nil — всё, что прошло общие проверки, считается OK
	// ChallengeIsMirrorDown — проверка Cloudflare значит «зеркало недоступно»: для трекеров,
	// которым пропуск не добыть (у Rutor нет Edge), лучше перейти на другое зеркало.
	ChallengeIsMirrorDown bool
	Rate                  rate.Limit     // запросов в секунду на трекер; 0 — 1 (спека, раздел 5)
	Timeout               time.Duration  // на одну попытку вместе с чтением ответа; 0 — 90 с
	Log                   *slog.Logger   // nil — без журнала
	Jar                   http.CookieJar // nil — без cookie
	Limiter               *rate.Limiter  // общий ограничитель с другим клиентом того же трекера; nil — свой по Rate
}

// Client — HTTP-клиент одного трекера: перебор зеркал, классификация ответов, повтор,
// ограничение частоты. Безопасен для одновременного использования. Без зеркал (адрес трекера
// не введён) никуда не ходит и отвечает ErrNotConfigured.
type Client struct {
	o    Options
	http *http.Client
	lim  *rate.Limiter

	mu      sync.Mutex
	mirrors []string        // адреса без «/» на конце; меняет SetMirrors
	own     map[string]bool // свои хосты в нижнем регистре: зеркала и дополнительные хосты
	current int             // номер зеркала, ответившего последним
	ua      string          // User-Agent запросов; меняет SetUserAgent
}

// maxBody — предел ответа: страницы трекеров — сотни КБ, .torrent — единицы МБ.
const maxBody = 32 << 20

func NewClient(o Options) (*Client, error) {
	mirrors, own, err := addresses(o.Mirrors, o.ExtraHosts)
	if err != nil {
		return nil, err
	}
	if o.Rate == 0 {
		o.Rate = 1
	}
	if o.Timeout == 0 {
		o.Timeout = 90 * time.Second
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	tr := NewTransport(o.Proxy)
	lim := o.Limiter
	if lim == nil {
		lim = rate.NewLimiter(o.Rate, 1)
	}
	c := &Client{o: o, http: &http.Client{Transport: tr, Jar: o.Jar}, lim: lim, mirrors: mirrors, own: own, ua: o.UserAgent}
	return c, nil
}

// addresses — своя копия зеркал без «/» на конце (адрес зеркала склеивается с путём и служит
// ключом cookie) и набор своих хостов.
func addresses(mirrors, extraHosts []string) ([]string, map[string]bool, error) {
	out := make([]string, len(mirrors))
	own := map[string]bool{}
	for i, m := range mirrors {
		out[i] = strings.TrimRight(m, "/")
		u, err := url.Parse(out[i])
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return nil, nil, fmt.Errorf("netx: зеркало %q — не адрес сайта (нужно https://…)", m)
		}
		own[strings.ToLower(u.Host)] = true
	}
	for _, h := range extraHosts {
		own[strings.ToLower(h)] = true
	}
	return out, own, nil
}

// SetMirrors меняет адреса на ходу (адрес трекера поменяли в настройках): следующий запрос идёт
// на новые зеркала, свои хосты — новые зеркала и extraHosts. Пусто — трекер выключен. Неверный
// адрес — ошибка, прежние адреса остаются.
func (c *Client) SetMirrors(mirrors []string, extraHosts ...string) error {
	ms, own, err := addresses(mirrors, extraHosts)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.mirrors, c.own, c.current = ms, own, 0
	c.mu.Unlock()
	return nil
}

// Configured — адрес трекера введён.
func (c *Client) Configured() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.mirrors) > 0
}

// Mirror — зеркало, которое ответило последним: с него начнётся следующий запрос. "" — адрес не введён.
func (c *Client) Mirror() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.mirrors) == 0 {
		return ""
	}
	return c.mirrors[c.current]
}

// GetOption — необязательный параметр Get.
type GetOption func(*getOpts)

type getOpts struct {
	noLimit, noClassify bool
	method, form        string // "" — GET; POST — с телом формы
}

// WithoutClassify — вернуть страницу без признаков трекера (общие проверки остаются): ответ
// разбирает сам вызывающий, например страницу после входа.
func WithoutClassify() GetOption { return func(g *getOpts) { g.noClassify = true } }

// Post — как Get, но POST формы (application/x-www-form-urlencoded). Тело — строкой: при
// повторе и на другом зеркале запрос собирается заново.
func (c *Client) Post(ctx context.Context, path, form string, opts ...GetOption) (*Page, error) {
	return c.Get(ctx, path, append(opts, func(g *getOpts) { g.method, g.form = http.MethodPost, form })...)
}

// WithoutLimit — запрос мимо ограничителя «1 в секунду». Только для параллельного поиска
// Rutor (спека, разделы 5 и 7): там одновременно идут не больше трёх запросов.
func WithoutLimit() GetOption { return func(g *getOpts) { g.noLimit = true } }

// target — куда слать запрос; mirror = -1 для полного адреса (без перебора зеркал), base — адрес
// зеркала: пока шёл запрос, адреса могли поменять (SetMirrors).
type target struct {
	url    string
	mirror int
	base   string
}

// Get запрашивает path на рабочем зеркале, при «зеркало недоступно» — на следующих.
// path — путь ("/browse/0/12/0/2") или полный адрес (тогда без перебора зеркал).
// Ошибки проверяются через errors.Is: ErrProxyDown, ErrTrackerDown, ErrChallenge,
// ErrLoginRequired, ErrRemoved; отмена ctx возвращается как есть.
func (c *Client) Get(ctx context.Context, path string, opts ...GetOption) (*Page, error) {
	var g getOpts
	for _, o := range opts {
		o(&g)
	}
	var down []string // почему не ответило каждое зеркало — для текста ошибки
	foreign := true   // все зеркала — чужие сайты (а не молчат)
	targets := c.targets(path)
	if targets == nil {
		return nil, fmt.Errorf("%s: %w", c.o.Name, ErrNotConfigured)
	}
	if g.method == http.MethodPost {
		targets = targets[:1] // форму — только на текущее зеркало: вход на другом — уже другая попытка
	}
	for _, t := range targets {
		host := hostOf(t.url)
		p, err := c.load(ctx, t.url, g)
		var de *downError
		if errors.As(err, &de) {
			down = append(down, host+" — "+de.reason)
			foreign = false
			args := []any{"mirror", host, "reason", de.reason}
			if de.raw != nil {
				args = append(args, "err", de.raw)
			}
			c.o.Log.Warn(c.o.Name+": зеркало не отвечает", args...)
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", c.o.Name, err)
		}
		v, reason := c.judge(p, g)
		if v == MirrorDown && reason == stubReason && g.method != http.MethodPost {
			// Короткая страница без каркаса бывает разовой (Rutracker 2026-10-02: 1,5 КБ вместо 96) — ещё раз здесь же.
			c.o.Log.Info(c.o.Name+": повторяю запрос", "url", t.url, "reason", reason)
			p2, err2 := c.load(ctx, t.url, g)
			var de2 *downError
			switch {
			case err2 == nil:
				p = p2
				v, reason = c.judge(p, g)
			case errors.As(err2, &de2):
				reason = de2.reason // вторая попытка не ответила — это и причина
			default:
				return nil, fmt.Errorf("%s: %w", c.o.Name, err2) // отмена, прокси — как есть (ревью 15Д)
			}
		}
		if v == MirrorDown {
			down = append(down, host+" — "+reason)
			foreign = foreign && strings.Contains(reason, "чуж")
			c.o.Log.Warn(c.o.Name+": зеркало не отвечает", "mirror", host, "reason", reason)
			continue
		}
		// Зеркало живо, даже если ответило «удалена» или «нужен вход», — с него и продолжаем.
		if t.mirror >= 0 {
			c.mu.Lock()
			if t.mirror < len(c.mirrors) && c.mirrors[t.mirror] == t.base {
				c.current = t.mirror
			}
			c.mu.Unlock()
		}
		switch v {
		case Challenge:
			return nil, fmt.Errorf("%s (%s): %w", c.o.Name, host, ErrChallenge)
		case LoginRequired:
			return nil, fmt.Errorf("%s (%s): %w", c.o.Name, host, ErrLoginRequired)
		case Removed:
			return nil, fmt.Errorf("%s: %w", c.o.Name, ErrRemoved)
		}
		return p, nil
	}
	return nil, &trackerDownError{name: c.o.Name, reasons: down, foreign: foreign && len(down) > 0}
}

// targets — адреса попыток по порядку; nil — адрес трекера не введён.
func (c *Client) targets(path string) []target {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := len(c.mirrors)
	if n == 0 {
		return nil
	}
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return []target{{path, -1, ""}}
	}
	out := make([]target, 0, n)
	for i := range n {
		m := (c.current + i) % n
		out = append(out, target{c.mirrors[m] + path, m, c.mirrors[m]})
	}
	return out
}

// isOwn — хост свой: зеркало или дополнительный хост трекера.
func (c *Client) isOwn(host string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.own[strings.ToLower(host)]
}

// stubReason — признаки трекера сказали «не он»: заглушка провайдера, чужая или оборванная страница.
const stubReason = "вместо трекера — заглушка или чужая страница"

// judge — сначала общие признаки из спеки, затем признаки трекера.
func (c *Client) judge(p *Page, g getOpts) (Verdict, string) {
	if !c.isOwn(p.URL.Host) {
		return MirrorDown, "перенаправляет на чужой сайт " + p.URL.Host
	}
	// Проверка Cloudflare приходит с кодом 403 или 503 — её смотрим раньше, чем 5xx.
	if isChallenge(p) {
		if c.o.ChallengeIsMirrorDown {
			return MirrorDown, "проверка Cloudflare"
		}
		return Challenge, ""
	}
	if p.Status >= 500 || p.Status == http.StatusUnavailableForLegalReasons {
		return MirrorDown, fmt.Sprintf("ответ %d", p.Status)
	}
	if c.o.Classify == nil || g.noClassify {
		return OK, ""
	}
	v := c.o.Classify(p)
	if v == MirrorDown {
		return v, stubReason
	}
	return v, ""
}

// isChallenge — проверка Cloudflare: главный признак — заголовок cf-mitigated
// (спека, раздел 6), запасные — заголовок страницы и скрипт проверки.
func isChallenge(p *Page) bool {
	if p.Status != http.StatusForbidden && p.Status != http.StatusServiceUnavailable {
		return false
	}
	if strings.EqualFold(p.Header.Get("Cf-Mitigated"), "challenge") {
		return true
	}
	return bytes.Contains(p.Body, []byte("<title>Just a moment")) ||
		bytes.Contains(p.Body, []byte("<title>Один момент")) ||
		bytes.Contains(p.Body, []byte("window._cf_chl_opt"))
}

// load — запрос с одним повтором, если ответ не пришёл за Timeout или оборвался:
// Rutor через VPN отвечает до 30 с и однажды оборвал ответ на 60 с (спека, раздел 5).
// Не вышло и со второго раза — зеркало недоступно.
func (c *Client) load(ctx context.Context, rawURL string, g getOpts) (*Page, error) {
	p, err := c.once(ctx, rawURL, g)
	var re *retryError
	if !errors.As(err, &re) {
		return p, err
	}
	// Форму не повторяем: сервер мог её уже принять (у Rutracker лишний вход приближает капчу).
	if g.method == http.MethodPost {
		return nil, &downError{reason: re.reason}
	}
	c.o.Log.Info(c.o.Name+": повторяю запрос", "url", rawURL, "reason", re.reason)
	p, err = c.once(ctx, rawURL, g)
	if errors.As(err, &re) {
		return nil, &downError{reason: re.reason}
	}
	return p, err
}

// once — одна попытка. Ошибки: *retryError — стоит повторить на этом же зеркале;
// *downError — зеркало недоступно; остальные (прокси, отмена, размер) — сразу наверх.
func (c *Client) once(ctx context.Context, rawURL string, g getOpts) (*Page, error) {
	if !g.noLimit {
		if err := c.lim.Wait(ctx); err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			// rate отказывает сразу, если очередь не успевает до срока ctx. Для вызывающего это то
			// же, что «время вышло», а не сбой трекера.
			return nil, fmt.Errorf("очередь запросов не успевает до срока: %w", context.DeadlineExceeded)
		}
	}
	actx, cancel := context.WithTimeout(ctx, c.o.Timeout)
	defer cancel()
	method, reqBody := http.MethodGet, io.Reader(nil)
	if g.method == http.MethodPost {
		method, reqBody = http.MethodPost, strings.NewReader(g.form)
	}
	req, err := http.NewRequestWithContext(actx, method, rawURL, reqBody)
	if err != nil {
		return nil, err
	}
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	c.mu.Lock()
	ua := c.ua
	c.mu.Unlock()
	if ua != "" {
		req.Header.Set("User-Agent", ua)
	}
	req.Header.Set("Accept-Language", "ru-RU,ru;q=0.9")
	resp, err := c.http.Do(req)
	if err != nil {
		switch {
		case ctx.Err() != nil:
			return nil, ctx.Err()
		case errors.Is(err, ErrProxyDown):
			return nil, err
		case proxyRefusal(err) != nil:
			return nil, proxyRefusal(err)
		case actx.Err() != nil:
			return nil, &retryError{"нет ответа за " + seconds(c.o.Timeout)}
		}
		return nil, &downError{netReason(err), err}
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusProxyAuthRequired { // обычный http:// через HTTP-прокси
		return nil, errProxyAuth
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		switch {
		case ctx.Err() != nil:
			return nil, ctx.Err()
		case actx.Err() != nil:
			return nil, &retryError{"нет ответа за " + seconds(c.o.Timeout)}
		}
		return nil, &retryError{"ответ оборвался"}
	}
	if len(body) > maxBody {
		return nil, fmt.Errorf("ответ %s больше %d МБ", req.URL.Host, maxBody>>20)
	}
	return &Page{URL: resp.Request.URL, Status: resp.StatusCode, Header: resp.Header, Body: body}, nil
}

type retryError struct{ reason string }

func (e *retryError) Error() string { return e.reason }

type downError struct {
	reason string
	raw    error // исходная ошибка — только для журнала; nil, если причина и так понятна
}

func (e *downError) Error() string { return e.reason }

// trackerDownError — не ответило ни одно зеркало; текст называет каждое и причину.
type trackerDownError struct {
	name    string
	reasons []string
	foreign bool // все зеркала — чужие сайты: ErrNotTracker
}

func (e *trackerDownError) Error() string {
	return fmt.Sprintf("%s недоступен (%s)", e.name, strings.Join(e.reasons, "; "))
}

func (e *trackerDownError) Is(target error) bool {
	return target == ErrTrackerDown || (e.foreign && target == ErrNotTracker)
}

// netReason — причина сетевой ошибки коротко и по-русски: текст уходит в «Проблемы».
// Сырой текст Go («read tcp …: wsarecv: …») остаётся только в журнале.
func netReason(err error) string {
	var dnsErr *net.DNSError
	var certErr *tls.CertificateVerificationError
	var alert tls.AlertError
	var opErr *net.OpError
	switch {
	case errors.As(err, &dnsErr):
		return "адрес не найден (DNS)"
	case errors.As(err, &certErr):
		return "ошибка сертификата TLS"
	case errors.As(err, &opErr) && opErr.Op == "dial":
		return "не удаётся подключиться"
	case errors.Is(err, syscall.ECONNRESET): // на Windows это и есть WSAECONNRESET
		return "соединение сброшено"
	case errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF):
		return "соединение закрылось без ответа"
	case errors.As(err, &alert) || strings.Contains(err.Error(), "tls: "):
		return "ошибка TLS"
	case strings.Contains(err.Error(), "stopped after"):
		return "слишком много перенаправлений"
	}
	return "сетевая ошибка"
}

func hostOf(raw string) string {
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		return u.Host
	}
	return raw
}

func seconds(d time.Duration) string { return fmt.Sprintf("%g с", d.Seconds()) }

// UserAgent — User-Agent запросов сейчас.
func (c *Client) UserAgent() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ua
}

// SetUserAgent меняет User-Agent следующих запросов: пропуск Cloudflare привязан к UA браузера,
// а Edge мог обновиться, пока служба работает (ревью этапа 4).
func (c *Client) SetUserAgent(ua string) {
	c.mu.Lock()
	c.ua = ua
	c.mu.Unlock()
}
