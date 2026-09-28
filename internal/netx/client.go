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
	"time"

	"golang.org/x/time/rate"
)

// Verdict — вывод об ответе трекера (спека, раздел 5, «Классификация ответа трекера»).
// «Каркас есть, нужного блока нет» сюда не входит: это ошибка разбора страницы
// (source.ErrParse), её возвращает парсер трекера, и зеркало при ней не меняется.
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
)

// Page — ответ трекера целиком.
type Page struct {
	URL    *url.URL // итоговый адрес — после редиректов
	Status int
	Header http.Header
	Body   []byte
}

type Options struct {
	Name       string        // «Rutor» — для текстов ошибок и журнала
	Mirrors    []string      // базовые адреса зеркал по порядку предпочтения: "https://rutor.info"
	ExtraHosts []string      // другие свои хосты (d.rutor.info): редирект туда — не «чужой сайт»
	Proxy      string        // прокси из настроек; пусто — напрямую
	UserAgent  string        // пусто — User-Agent Go по умолчанию
	Classify   ClassifyFunc  // nil — всё, что прошло общие проверки, считается OK
	Rate       rate.Limit    // запросов в секунду на трекер; 0 — 1 (спека, раздел 5)
	Timeout    time.Duration // на одну попытку вместе с чтением ответа; 0 — 90 с
	Log        *slog.Logger  // nil — без журнала
}

// Client — HTTP-клиент одного трекера: перебор зеркал, классификация ответов, повтор,
// ограничение частоты. Безопасен для одновременного использования.
type Client struct {
	o    Options
	http *http.Client
	lim  *rate.Limiter
	own  map[string]bool // свои хосты в нижнем регистре: зеркала и ExtraHosts

	mu      sync.Mutex
	current int // номер зеркала, ответившего последним
}

// maxBody — предел ответа: страницы трекеров — сотни КБ, .torrent — единицы МБ.
const maxBody = 32 << 20

func NewClient(o Options) (*Client, error) {
	if len(o.Mirrors) == 0 {
		return nil, fmt.Errorf("netx: у трекера %q нет зеркал", o.Name)
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
	tr, err := NewTransport(o.Proxy)
	if err != nil {
		return nil, err
	}
	c := &Client{o: o, http: &http.Client{Transport: tr}, lim: rate.NewLimiter(o.Rate, 1), own: map[string]bool{}}
	for _, m := range o.Mirrors {
		u, err := url.Parse(m)
		if err != nil || u.Host == "" {
			return nil, fmt.Errorf("netx: зеркало %q — не адрес сайта (нужно https://…)", m)
		}
		c.own[strings.ToLower(u.Host)] = true
	}
	for _, h := range o.ExtraHosts {
		c.own[strings.ToLower(h)] = true
	}
	return c, nil
}

// Mirror — зеркало, которое ответило последним: с него начнётся следующий запрос.
func (c *Client) Mirror() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.o.Mirrors[c.current]
}

// GetOption — необязательный параметр Get.
type GetOption func(*getOpts)

type getOpts struct{ noLimit bool }

// WithoutLimit — запрос мимо ограничителя «1 в секунду». Только для параллельного поиска
// Rutor (спека, разделы 5 и 7): там одновременно идут не больше трёх запросов.
func WithoutLimit() GetOption { return func(g *getOpts) { g.noLimit = true } }

// target — куда слать запрос; mirror = -1 для полного адреса (без перебора зеркал).
type target struct {
	url    string
	mirror int
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
	for _, t := range c.targets(path) {
		host := hostOf(t.url)
		p, err := c.load(ctx, t.url, g)
		var de *downError
		if errors.As(err, &de) {
			down = append(down, host+" — "+de.reason)
			c.o.Log.Warn(c.o.Name+": зеркало не отвечает", "mirror", host, "reason", de.reason)
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", c.o.Name, err)
		}
		v, reason := c.judge(p)
		if v == MirrorDown {
			down = append(down, host+" — "+reason)
			c.o.Log.Warn(c.o.Name+": зеркало не отвечает", "mirror", host, "reason", reason)
			continue
		}
		// Зеркало живо, даже если ответило «удалена» или «нужен вход», — с него и продолжаем.
		if t.mirror >= 0 {
			c.mu.Lock()
			c.current = t.mirror
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
	return nil, &trackerDownError{name: c.o.Name, reasons: down}
}

func (c *Client) targets(path string) []target {
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return []target{{path, -1}}
	}
	c.mu.Lock()
	start := c.current
	c.mu.Unlock()
	n := len(c.o.Mirrors)
	out := make([]target, 0, n)
	for i := range n {
		m := (start + i) % n
		out = append(out, target{strings.TrimRight(c.o.Mirrors[m], "/") + path, m})
	}
	return out
}

// judge — сначала общие признаки из спеки, затем признаки трекера.
func (c *Client) judge(p *Page) (Verdict, string) {
	if !c.own[strings.ToLower(p.URL.Host)] {
		return MirrorDown, "перенаправляет на чужой сайт " + p.URL.Host
	}
	// Проверка Cloudflare приходит с кодом 403 или 503 — её смотрим раньше, чем 5xx.
	if isChallenge(p) {
		return Challenge, ""
	}
	if p.Status >= 500 || p.Status == http.StatusUnavailableForLegalReasons {
		return MirrorDown, fmt.Sprintf("ответ %d", p.Status)
	}
	if c.o.Classify == nil {
		return OK, ""
	}
	v := c.o.Classify(p)
	if v == MirrorDown {
		return v, "вместо трекера — заглушка или чужая страница"
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
	c.o.Log.Info(c.o.Name+": повторяю запрос", "url", rawURL, "reason", re.reason)
	p, err = c.once(ctx, rawURL, g)
	if errors.As(err, &re) {
		return nil, &downError{re.reason}
	}
	return p, err
}

// once — одна попытка. Ошибки: *retryError — стоит повторить на этом же зеркале;
// *downError — зеркало недоступно; остальные (прокси, отмена, размер) — сразу наверх.
func (c *Client) once(ctx context.Context, rawURL string, g getOpts) (*Page, error) {
	if !g.noLimit {
		if err := c.lim.Wait(ctx); err != nil {
			return nil, err
		}
	}
	actx, cancel := context.WithTimeout(ctx, c.o.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(actx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	if c.o.UserAgent != "" {
		req.Header.Set("User-Agent", c.o.UserAgent)
	}
	req.Header.Set("Accept-Language", "ru-RU,ru;q=0.9")
	resp, err := c.http.Do(req)
	if err != nil {
		switch {
		case ctx.Err() != nil:
			return nil, ctx.Err()
		case errors.Is(err, ErrProxyDown):
			return nil, err
		case actx.Err() != nil:
			return nil, &retryError{"нет ответа за " + seconds(c.o.Timeout)}
		}
		return nil, &downError{netReason(err)}
	}
	defer resp.Body.Close()
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

type downError struct{ reason string }

func (e *downError) Error() string { return e.reason }

// trackerDownError — не ответило ни одно зеркало; текст называет каждое и причину.
type trackerDownError struct {
	name    string
	reasons []string
}

func (e *trackerDownError) Error() string {
	return fmt.Sprintf("%s недоступен (%s)", e.name, strings.Join(e.reasons, "; "))
}

func (e *trackerDownError) Is(target error) bool { return target == ErrTrackerDown }

// netReason — причина сетевой ошибки человеческими словами.
func netReason(err error) string {
	var dnsErr *net.DNSError
	var certErr *tls.CertificateVerificationError
	var opErr *net.OpError
	switch {
	case errors.As(err, &dnsErr):
		return "адрес не найден (DNS)"
	case errors.As(err, &certErr):
		return "ошибка сертификата TLS"
	case errors.As(err, &opErr) && opErr.Op == "dial":
		return "не удаётся подключиться"
	}
	return err.Error()
}

func hostOf(raw string) string {
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		return u.Host
	}
	return raw
}

func seconds(d time.Duration) string { return fmt.Sprintf("%g с", d.Seconds()) }
