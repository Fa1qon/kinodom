// Package jacred — источник поиска Jacred или Jackett по адресу, который ввёл пользователь (спека 11b,
// раздел 8; исследование, 22.2): один адаптер Jackett-совместимого JSON для jac.red, своего Jacred и
// Jackett. Только поиск: каталога, топов и страниц раздач у источника нет.
package jacred

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/anacrolix/torrent/metainfo"

	"kinodom/internal/meta"
	"kinodom/internal/netx"
	"kinodom/internal/source"
)

const (
	resultsPath = "/api/v2.0/indexers/all/results" // Jackett-совместимый поиск (и у Jacred)
	confPath    = "/api/v1.0/conf"                 // только у Jacred: {"jacred":true,"apikey":…}
	maxBody     = 32 << 20                         // ответ карточкой с ffprobe — до 0,5 МБ (22.2)
)

// Причины для строки трекеров поиска и для «Проверить» (errors.Is).
var (
	ErrNeedKey   = errors.New("нужен ключ")
	ErrBadKey    = errors.New("ключ не подошёл")
	ErrDown      = errors.New("не отвечает")
	ErrNotSource = errors.New("адрес не похож на Jacred или Jackett")
)

type Options struct {
	Address, Key string        // пусто — источник выключен; ключ — если сервис просит
	Proxy        *netx.Proxy   // прокси трекеров из настроек; nil — напрямую
	Timeout      time.Duration // на запрос; 0 — 25 с
	Log          *slog.Logger  // nil — без журнала
}

// Client — источник поиска. Адрес запроса несёт ключ (параметр apikey), поэтому адрес запроса не пишется
// ни в журнал, ни в текст ошибки — в отличие от netx.Client трекеров.
type Client struct {
	o  Options
	hc *http.Client

	mu        sync.Mutex
	addr, key string
}

func New(o Options) *Client {
	if o.Timeout == 0 {
		o.Timeout = 25 * time.Second
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	t := netx.NewTransport(o.Proxy)
	if p := o.Proxy; p != nil {
		// Свой Jacred или Jackett в домашней сети — напрямую: прокси (часто — VPN-клиент) туда не ведёт.
		t.Proxy = func(r *http.Request) (*url.URL, error) {
			if netx.PrivateHost(r.URL.Hostname()) {
				return nil, nil
			}
			return p.ForRequest(r)
		}
	}
	c := &Client{o: o, hc: &http.Client{Transport: t}}
	c.SetAddress(o.Address, o.Key)
	return c
}

// SetAddress — настройки сменили на ходу; пустой адрес выключает источник.
func (c *Client) SetAddress(addr, key string) {
	c.mu.Lock()
	c.addr, c.key = strings.TrimRight(strings.TrimSpace(addr), "/"), strings.TrimSpace(key)
	c.mu.Unlock()
}

func (c *Client) address() (string, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.addr, c.key
}

func (c *Client) Configured() bool {
	addr, _ := c.address()
	return addr != ""
}

func (c *Client) Name() string { return "jacred" }

// Query — запрос поиска, разобранный для карточки источника.
type Query struct {
	Title        string
	Year, Season int // 0 — нет
}

var (
	reSeasonTok = regexp.MustCompile(`(?i)^s(\d{1,2})(?:e\d{1,3})?$`)
	reSeasonNum = regexp.MustCompile(`^(\d{1,2})(?:-?й|-?ый)?$`)
	reYearTok   = regexp.MustCompile(`^(?:19|20)\d{2}$`)
)

// ParseQuery — название, год и сезон из запроса: «Джентльмены 2 сезон», «Матрица 1999», «The Gentlemen
// S02». Год — только если кроме него есть название и он не в будущем («1917», «Бегущий по лезвию 2049» —
// названия).
func ParseQuery(q string) Query {
	var out Query
	toks := strings.Fields(q)
	var keep []string
	yearAt := -1
	for i := 0; i < len(toks); i++ {
		w := strings.Trim(toks[i], "()[],.;")
		lw := strings.ToLower(strings.TrimSuffix(w, ":"))
		next := ""
		if i+1 < len(toks) {
			next = strings.ToLower(strings.Trim(toks[i+1], "()[],.;:"))
		}
		if m := reSeasonTok.FindStringSubmatch(w); m != nil {
			out.Season, _ = strconv.Atoi(m[1])
			continue
		}
		if m := reSeasonNum.FindStringSubmatch(next); m != nil && seasonWord(lw) {
			out.Season, _ = strconv.Atoi(m[1])
			i++
			continue
		}
		if m := reSeasonNum.FindStringSubmatch(lw); m != nil && seasonWord(next) {
			out.Season, _ = strconv.Atoi(m[1])
			i++
			continue
		}
		if y, _ := strconv.Atoi(w); reYearTok.MatchString(w) && y <= time.Now().Year()+1 {
			yearAt, out.Year = len(keep), y
		}
		keep = append(keep, toks[i])
	}
	if yearAt >= 0 && len(keep) > 1 {
		keep = slices.Delete(keep, yearAt, yearAt+1)
	} else {
		out.Year = 0
	}
	out.Title = strings.Trim(strings.Join(keep, " "), " ,.;:-")
	return out
}

func seasonWord(w string) bool { return w == "сезон" || w == "сезона" || w == "season" }

// Search — раздачи по запросу: источник спрашивается карточкой (название — оно же оригинальное: запрос
// бывает и по-английски; год; сериал, если назван сезон), сезон отбирается у нас — по info.seasons, а
// без них — по названию раздачи. Исследование 22.2: свободным текстом находилось 2 раздачи вместо 118.
func (c *Client) Search(ctx context.Context, query string) ([]source.Release, error) {
	addr, key := c.address()
	if addr == "" {
		return nil, source.ErrNotConfigured
	}
	q := ParseQuery(query)
	if q.Title == "" {
		return nil, nil
	}
	resp, err := c.search(ctx, addr, key, q)
	if err != nil {
		return nil, err
	}
	if len(resp.Results) == 0 && resp.Jacred && key == "" && c.keyRequired(ctx, addr) {
		return nil, ErrNeedKey // Jacred с обязательным ключом без ключа отдаёт пустой список: это не «ничего не нашлось»
	}
	out := make([]source.Release, 0, len(resp.Results))
	for _, r := range resp.Results {
		if rel, ok := r.release(resp.Jacred); ok && (q.Season == 0 || r.hasSeason(q.Season)) {
			out = append(out, rel)
		}
	}
	return out, nil
}

// Check — «Проверить»: Jacred узнаётся по /api/v1.0/conf, Jackett — пробным поиском; итог — текст для пульта.
func (c *Client) Check(ctx context.Context) (ok bool, text string) {
	addr, key := c.address()
	if addr == "" {
		return false, "Укажите адрес источника"
	}
	trial := Query{Title: "Матрица", Year: 1999}
	conf, err := c.conf(ctx, addr)
	switch {
	case errors.Is(err, ErrDown):
		return false, checkText(err)
	case err == nil && conf.Jacred:
		resp, err := c.search(ctx, addr, key, trial)
		if err != nil {
			return false, checkText(err)
		}
		// Jacred с обязательным ключом без ключа и с чужим ключом отвечает пустым списком, не 401. «apikey» в
		// conf — только «ключ на сервере задан»: jac.red с ним ищет и без ключа (вживую 2026-10-01).
		if len(resp.Results) == 0 && conf.APIKey {
			if key == "" {
				return false, "Нужен ключ"
			}
			return false, "Ключ не подошёл"
		}
		return true, "Jacred отвечает"
	}
	if _, err := c.search(ctx, addr, key, trial); err != nil {
		return false, checkText(err)
	}
	return true, "Jackett отвечает"
}

// keyRequired — Jacred без ключа ничего не находит: на сервере задан ключ (conf) и пробный поиск «Матрица 1999»
// без ключа пуст.
func (c *Client) keyRequired(ctx context.Context, addr string) bool {
	conf, err := c.conf(ctx, addr)
	if err != nil || !conf.APIKey {
		return false
	}
	resp, err := c.search(ctx, addr, "", Query{Title: "Матрица", Year: 1999})
	return err == nil && len(resp.Results) == 0
}

func checkText(err error) string {
	switch {
	case errors.Is(err, ErrNeedKey):
		return "Нужен ключ"
	case errors.Is(err, ErrBadKey):
		return "Ключ не подошёл"
	case errors.Is(err, netx.ErrProxyDown):
		return "Прокси не отвечает"
	case errors.Is(err, ErrDown):
		return "Не отвечает — нужен прокси?"
	case errors.Is(err, ErrNotSource):
		return "Адрес не похож на Jacred или Jackett"
	}
	s := err.Error()
	first, n := utf8.DecodeRuneInString(s)
	return string(unicode.ToUpper(first)) + s[n:]
}

// response — ответ поиска. Results — указатель: ответ без этого поля — не Jacred и не Jackett.
type response struct {
	Results *[]result `json:"Results"`
	Jacred  bool      `json:"jacred"`
}

type searchResponse struct {
	Results []result
	Jacred  bool
}

type confResponse struct {
	Jacred bool `json:"jacred"`
	APIKey bool `json:"apikey"`
}

func (c *Client) search(ctx context.Context, addr, key string, q Query) (searchResponse, error) {
	v := url.Values{"Query": {q.Title}, "title": {q.Title}, "title_original": {q.Title}}
	if q.Year > 0 {
		v.Set("year", strconv.Itoa(q.Year))
	}
	if q.Season > 0 {
		v.Set("is_serial", "2")
	}
	if key != "" {
		v.Set("apikey", key)
	}
	var resp response
	if err := c.get(ctx, addr, resultsPath, v, key != "", &resp); errors.Is(err, errNoPage) {
		return searchResponse{}, ErrNotSource // поиска по этому адресу нет: не Jacred и не Jackett (или другой путь)
	} else if err != nil {
		return searchResponse{}, err
	}
	if resp.Results == nil {
		return searchResponse{}, ErrNotSource
	}
	return searchResponse{Results: *resp.Results, Jacred: resp.Jacred}, nil
}

func (c *Client) conf(ctx context.Context, addr string) (confResponse, error) {
	var conf confResponse
	err := c.get(ctx, addr, confPath, nil, false, &conf)
	return conf, err
}

// errNoPage — 404: у Jackett нет /api/v1.0/conf.
var errNoPage = errors.New("страницы нет")

// get — GET addr+path, ответ JSON — в out. Адрес запроса (в нём ключ) не попадает ни в текст ошибки,
// ни в журнал: у ошибок net/http он внутри *url.Error, поэтому наружу уходит только её причина.
func (c *Client) get(ctx context.Context, addr, path string, v url.Values, withKey bool, out any) error {
	ctx, cancel := context.WithTimeout(ctx, c.o.Timeout)
	defer cancel()
	u := addr + path
	if len(v) > 0 {
		u += "?" + v.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return ErrNotSource
	}
	req.Header.Set("Accept", "application/json")
	host := req.URL.Host
	resp, err := c.hc.Do(req)
	if err != nil {
		return c.down(ctx, host, path, err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		if withKey {
			return ErrBadKey
		}
		return ErrNeedKey
	case resp.StatusCode == http.StatusNotFound:
		return errNoPage
	case resp.StatusCode != http.StatusOK:
		c.o.Log.Warn("источник поиска: ответ не 200", "host", host, "path", path, "status", resp.StatusCode)
		return fmt.Errorf("ответ сервера %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return c.down(ctx, host, path, err)
	}
	if len(body) > maxBody {
		return fmt.Errorf("ответ больше %d МБ", maxBody>>20)
	}
	if err := json.Unmarshal(body, out); err != nil {
		c.o.Log.Debug("источник поиска: ответ не JSON", "host", host, "path", path)
		return ErrNotSource
	}
	return nil
}

// down — нет ответа: причина коротко, без адреса запроса.
func (c *Client) down(ctx context.Context, host, path string, err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	c.o.Log.Debug("источник поиска: нет ответа", "host", host, "path", path, "err", err)
	reason := "нет соединения"
	switch {
	case errors.Is(err, netx.ErrProxyDown):
		reason = "прокси не отвечает"
	case ctx.Err() != nil:
		reason = fmt.Sprintf("нет ответа за %g с", c.o.Timeout.Seconds())
	}
	return &downError{reason: reason, err: err}
}

type downError struct {
	reason string
	err    error // причина без адреса запроса: для errors.Is (ErrProxyDown, DeadlineExceeded)
}

func (e *downError) Error() string        { return "не отвечает (" + e.reason + ")" }
func (e *downError) Is(target error) bool { return target == ErrDown }
func (e *downError) Unwrap() error        { return e.err }

// result — раздача ответа: поля Jacred и Jackett (TrackerId, InfoHash — только у Jackett; info — только у Jacred).
type result struct {
	Tracker     string `json:"Tracker"`   // Jacred: «bitru, torrentby, kinozal» — первый — тот, чья ссылка
	TrackerID   string `json:"TrackerId"` // Jackett: «kinozal», «rutracker»
	Details     string `json:"Details"`   // ссылка на тему
	Title       string `json:"Title"`
	Size        int64  `json:"Size"`
	PublishDate string `json:"PublishDate"`
	Seeders     int    `json:"Seeders"`
	Peers       int    `json:"Peers"` // Jacred — качающие, Jackett — все пиры
	MagnetURI   string `json:"MagnetUri"`
	Info        *struct {
		Seasons []int `json:"seasons"`
	} `json:"info"`
}

var (
	reRutorTopic     = regexp.MustCompile(`/torrent/(\d+)`)
	reRutrackerTopic = regexp.MustCompile(`viewtopic\.php\?t=(\d+)`)
)

// release — строка поиска. Rutor и Rutracker — номер темы из ссылки (наша раздача со страницей); чужой
// трекер — номер раздачи — infohash, нужен magnet: ссылка Jackett на .torrent несёт ключ и не берётся.
func (r result) release(jacred bool) (source.Release, bool) {
	name := r.TrackerID
	if name == "" {
		name, _, _ = strings.Cut(r.Tracker, ",")
	}
	rel := source.Release{
		Tracker: strings.ToLower(strings.TrimSpace(name)),
		Title:   strings.TrimSpace(r.Title),
		Seeders: r.Seeders, Leechers: r.Peers, Size: r.Size, Added: publishDate(r.PublishDate),
	}
	if !jacred {
		rel.Leechers = max(r.Peers-r.Seeders, 0)
	}
	if strings.HasPrefix(r.Details, "https://") || strings.HasPrefix(r.Details, "http://") {
		rel.Link = r.Details
	}
	if m, err := metainfo.ParseMagnetUri(r.MagnetURI); err == nil {
		rel.Magnet, rel.InfoHash = r.MagnetURI, m.InfoHash.HexString()
	}
	var topic *regexp.Regexp
	switch rel.Tracker {
	case "rutor":
		topic = reRutorTopic
	case "rutracker":
		topic = reRutrackerTopic
	}
	switch {
	case rel.Tracker == "" || rel.Title == "":
		return rel, false
	case topic != nil:
		m := topic.FindStringSubmatch(rel.Link)
		if m == nil {
			return rel, false
		}
		rel.TopicID = m[1]
	case rel.Magnet == "":
		return rel, false
	default:
		rel.TopicID = rel.InfoHash
	}
	return rel, true
}

// hasSeason — у раздачи есть сезон n: по info.seasons, без них — по названию; сезона не видно — оставить.
func (r result) hasSeason(n int) bool {
	if r.Info != nil && len(r.Info.Seasons) > 0 {
		return slices.Contains(r.Info.Seasons, n)
	}
	s := meta.SeasonNumber(meta.ParseTitle(r.Title).Season)
	return s == 0 || s == n
}

func publishDate(s string) time.Time {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	t, _ := time.ParseInLocation("2006-01-02T15:04:05.999999999", s, time.Local)
	return t
}
