package meta

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"kinodom/internal/netx"
)

// Кинопоиск без токена (спека 11b, раздел 5; исследование, раздел 22.4): GraphQL сайта и страница
// фильма. Сайт принимает только тексты запросов из своего белого списка — байт в байт, как их
// печатает сайт (сняты с frontend-www-release-1229.0); свой или переформатированный текст —
// «the query is not allowed».
//
//go:embed kpgql/*.graphql
var kpQueries embed.FS

var (
	DefaultKPGraphQL = "https://graphql.kinopoisk.ru/graphql/"
	DefaultKPSite    = "https://www.kinopoisk.ru"
)

var (
	ErrKPBlocked    = errors.New("Кинопоиск не отвечает")
	ErrKPDailyLimit = errors.New("Кинопоиск: суточный предел запросов исчерпан")
	errKPNotAllowed = errors.New("Кинопоиск: сайт больше не принимает этот запрос")
	errKPOpPaused   = errors.New("Кинопоиск: запрос отложен — сайт его не принимает")
)

// KPClass — очередь запроса: каталог (KPNormal) и правка в пульте (KPUrgent) идут раньше медиатеки
// (KPBackground; спека 11b, раздел 5.2). Каталогу — суточный предел без резерва: резерв — правке и
// медиатеке (ревью 11b-Б, Important 5).
type KPClass int

const (
	KPNormal KPClass = iota
	KPBackground
	KPUrgent
)

type KPWebOptions struct {
	GraphQL     string        // "" — DefaultKPGraphQL
	Site        string        // "" — DefaultKPSite
	Every       time.Duration // 0 — 3 с между запросами (GraphQL и страницы вместе)
	DailyLimit  int           // 0 — 500 запросов в сутки
	Reserve     int           // из них — только правке в пульте и медиатеке; 0 — пятая часть, < 0 — нет
	Pause       time.Duration // 0 — 6 ч после отказа
	Timeout     time.Duration // 0 — 30 с
	RatingBase  string        // оценки КП и IMDb по номеру (план 14Г); "" — DefaultKPRating
	RatingEvery time.Duration // между запросами оценок (свои ворота); 0 — 2 с
	Now         func() time.Time
	Log         *slog.Logger
}

// KPWebStatus — для «Состояния»: пауза поиска без токена (и её причина) и запросов за сутки.
type KPWebStatus struct {
	PausedUntil time.Time `json:"pausedUntil"`
	Reason      string    `json:"reason"`
	Today       int       `json:"today"`
	Limit       bool      `json:"limit"` // пауза — суточный предел, а не отказ сайта
}

// KPWeb — клиент сайта Кинопоиска без токена. Безопасен для одновременного использования.
type KPWeb struct {
	o    KPWebOptions
	http *http.Client

	mu            sync.Mutex
	next          time.Time // не раньше — следующий запрос (часы процесса)
	normalWaiting int
	day           string
	today         int
	blockedUntil  time.Time // отказ всему сайту: 403, 429, капча, страница вместо JSON, сбои подряд
	blockReason   string
	troubles      int                  // сбоев без отказа подряд (сеть, 5xx, непонятный ответ)
	opPaused      map[string]time.Time // запрос, который сайт больше не принимает
	ratingNext    time.Time            // оценки IMDb: не раньше — следующий запрос
	ratingBlocked time.Time            // оценки IMDb: отказ — пауза до
}

func NewKPWeb(o KPWebOptions) *KPWeb {
	if o.GraphQL == "" {
		o.GraphQL = DefaultKPGraphQL
	}
	if o.Site == "" {
		o.Site = DefaultKPSite
	}
	o.Site = strings.TrimRight(o.Site, "/")
	if o.Every == 0 {
		o.Every = 3 * time.Second
	}
	if o.DailyLimit == 0 {
		o.DailyLimit = 500
	}
	switch {
	case o.Reserve == 0:
		o.Reserve = o.DailyLimit / 5
	case o.Reserve < 0:
		o.Reserve = 0
	}
	if o.Pause == 0 {
		o.Pause = 6 * time.Hour
	}
	if o.Timeout == 0 {
		o.Timeout = 30 * time.Second
	}
	if o.RatingBase == "" {
		o.RatingBase = DefaultKPRating
	}
	o.RatingBase = strings.TrimRight(o.RatingBase, "/")
	if o.RatingEvery == 0 {
		o.RatingEvery = 2 * time.Second
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	return &KPWeb{o: o, http: &http.Client{Transport: netx.NewTransport(nil), Timeout: o.Timeout}, opPaused: map[string]time.Time{}}
}

// Status — пауза поиска без токена и запросов за сегодня. Суточный предел исчерпан — пауза до полуночи
// (Limit: это не отказ сайта; ревью 11b-Б).
func (w *KPWeb) Status() KPWebStatus {
	w.mu.Lock()
	defer w.mu.Unlock()
	now := w.o.Now()
	w.rollDay(now)
	until, reason, limit := w.blockedUntil, w.blockReason, false
	if t := w.opPaused[opSuggest]; t.After(until) {
		until, reason = t, "не принимает запросы поиска"
	}
	if !until.After(now) {
		until, reason = time.Time{}, ""
	}
	if w.today >= w.o.DailyLimit {
		y, m, d := now.Date()
		if midnight := time.Date(y, m, d+1, 0, 0, 0, 0, now.Location()); midnight.After(until) {
			until, reason, limit = midnight, "суточный предел", true
		}
	}
	return KPWebStatus{PausedUntil: until, Reason: reason, Today: w.today, Limit: limit}
}

func (w *KPWeb) rollDay(now time.Time) {
	if d := now.Format("2006-01-02"); d != w.day {
		w.day, w.today = d, 0
	}
}

// acquire — ворота: не чаще Every, не больше DailyLimit в сутки (каталогу — без резерва); KPBackground
// ждёт, пока есть ждущие KPNormal и KPUrgent.
func (w *KPWeb) acquire(ctx context.Context, class KPClass) error {
	limit := w.o.DailyLimit
	if class == KPNormal {
		limit -= w.o.Reserve
	}
	if class != KPBackground {
		w.mu.Lock()
		w.normalWaiting++
		w.mu.Unlock()
		defer func() {
			w.mu.Lock()
			w.normalWaiting--
			w.mu.Unlock()
		}()
	}
	for {
		w.mu.Lock()
		w.rollDay(w.o.Now())
		if w.today >= limit {
			w.mu.Unlock()
			return ErrKPDailyLimit
		}
		t := time.Now()
		var wait time.Duration
		if class == KPBackground && w.normalWaiting > 0 {
			wait = max(w.next.Sub(t), 5*time.Millisecond)
		} else if !t.Before(w.next) {
			w.next = t.Add(w.o.Every)
			w.today++
			w.mu.Unlock()
			return nil
		} else {
			wait = w.next.Sub(t)
		}
		w.mu.Unlock()
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// paused — отказ сайта ещё действует: ErrKPBlocked; запрос op сайт не принимает — errKPOpPaused.
func (w *KPWeb) paused(op string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	now := w.o.Now()
	if now.Before(w.blockedUntil) {
		return ErrKPBlocked
	}
	if now.Before(w.opPaused[op]) {
		return errKPOpPaused
	}
	return nil
}

func (w *KPWeb) block(reason string, d time.Duration) {
	w.mu.Lock()
	w.blockedUntil, w.blockReason, w.troubles = w.o.Now().Add(d), reason, 0
	until := w.blockedUntil
	w.mu.Unlock()
	w.o.Log.Warn("Кинопоиск без токена: пауза", "причина", reason, "до", until.Format("15:04"))
}

func (w *KPWeb) pauseOp(op string) {
	w.mu.Lock()
	w.opPaused[op] = w.o.Now().Add(w.o.Pause)
	w.mu.Unlock()
	w.o.Log.Warn("Кинопоиск без токена: сайт не принимает запрос — нужны новые тексты запросов", "запрос", op)
}

const (
	opSuggest = "SuggestSearch"
	opFilm    = "FilmBaseInfo"
	opSeries  = "TvSeriesBaseInfo"
)

var reCaptcha = regexp.MustCompile(`(?i)captcha`)

// Сбои без отказа (ревью 11b-Б, Important 3): столько подряд — пауза. Иначе сайт, который отвечает не
// так, как ждали (без российского адреса — неизвестно как), съедал бы суточный предел и откладывал
// задачи очереди на неделю, а «Состояние» показывало бы «работает».
const (
	kpTroubleLimit = 3
	kpTroublePause = time.Hour
)

// kpTrouble — сбой без отказа: сеть, 5xx, ответ не того вида. Текст — без адреса.
type kpTrouble struct {
	text string
	err  error
}

func (e kpTrouble) Error() string { return "Кинопоиск: " + e.text }
func (e kpTrouble) Unwrap() error { return e.err }

// settle — итог запроса для счёта сбоев подряд: kpTroubleLimit-й подряд — пауза, и вызывающий получает
// ErrKPBlocked; ответ по делу (данные, «нет такого», «не принимает запрос») счёт обнуляет.
func (w *KPWeb) settle(ctx context.Context, err error) error {
	var tr kpTrouble
	switch {
	case errors.As(err, &tr):
		if ctx.Err() != nil {
			return err
		}
		w.mu.Lock()
		w.troubles++
		n := w.troubles
		w.mu.Unlock()
		if n >= kpTroubleLimit {
			w.block("сбои подряд — "+tr.text, kpTroublePause)
			return ErrKPBlocked
		}
	case err == nil || errors.Is(err, errKPMaybeNotFound) || errors.Is(err, ErrNotFound) || errors.Is(err, errKPNotAllowed):
		w.mu.Lock()
		w.troubles = 0
		w.mu.Unlock()
	}
	return err
}

// gql — запрос op с переменными vars; data ответа — в out.
func (w *KPWeb) gql(ctx context.Context, class KPClass, op string, vars map[string]any, out any) (err error) {
	if err := w.paused(op); err != nil {
		return err
	}
	q, err := kpQueries.ReadFile("kpgql/" + op + ".graphql")
	if err != nil {
		return err
	}
	body, err := json.Marshal(struct {
		OperationName string         `json:"operationName"`
		Variables     map[string]any `json:"variables"`
		Query         string         `json:"query"`
	}{op, vars, string(q)})
	if err != nil {
		return err
	}
	if err := w.acquire(ctx, class); err != nil {
		return err
	}
	defer func() { err = w.settle(ctx, err) }()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.o.GraphQL+"?operationName="+op, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("service-id", "25")
	req.Header.Set("x-request-id", fmt.Sprintf("%d%07d", time.Now().UnixMilli(), rand.IntN(9_000_000)+1_000_000))
	b, status, err := w.do(req)
	if err != nil {
		return err
	}
	var env struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message    string `json:"message"`
			Extensions struct {
				Code string `json:"code"`
			} `json:"extensions"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(b, &env); err != nil {
		page := bytes.HasPrefix(bytes.TrimSpace(b), []byte("<"))
		switch {
		case page && reCaptcha.Match(b):
			w.block("капча", w.o.Pause)
			return ErrKPBlocked
		case status >= 500:
			return kpTrouble{text: fmt.Sprintf("сбой сервиса (ответ %d)", status)}
		case page:
			// Вход, заглушка, отказ по адресу — сайт не даёт данных (ревью 11b-Б, Important 3).
			w.block(fmt.Sprintf("вместо данных — страница (ответ %d)", status), w.o.Pause)
		default:
			w.block(fmt.Sprintf("непонятный ответ (%d)", status), w.o.Pause)
		}
		return ErrKPBlocked
	}
	notFound := false
	for _, e := range env.Errors {
		if e.Message == "the query is not allowed" {
			return errKPNotAllowed
		}
		notFound = notFound || e.Extensions.Code == "NotFoundError"
	}
	if status != http.StatusOK {
		return kpTrouble{text: fmt.Sprintf("ответ %d", status)}
	}
	if len(env.Data) == 0 || string(env.Data) == "null" {
		return kpTrouble{text: "пустой ответ"}
	}
	if err := json.Unmarshal(env.Data, out); err != nil {
		return kpTrouble{text: "непонятный ответ: " + err.Error(), err: err}
	}
	if notFound {
		return errKPMaybeNotFound
	}
	return nil
}

// errKPMaybeNotFound — в ответе есть NotFoundError: пустое ли нужное поле, решает вызывающий (у
// фильма сайт заодно ищет сериал с тем же номером и всегда жалуется на него).
var errKPMaybeNotFound = errors.New("Кинопоиск: частично не найдено")

// do — запрос с разбором отказов: 403 и 429 — пауза сайту. Текст ошибки — без адреса.
func (w *KPWeb) do(req *http.Request) ([]byte, int, error) {
	resp, err := w.http.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return nil, 0, kpTrouble{text: err.Error(), err: err}
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
		w.block("ответ "+strconv.Itoa(resp.StatusCode), w.o.Pause)
		return nil, resp.StatusCode, ErrKPBlocked
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, resp.StatusCode, kpTrouble{text: "ответ оборвался"}
	}
	return b, resp.StatusCode, nil
}

// kpMovie — фильм или сериал в ответах GraphQL.
type kpMovie struct {
	Typename       string `json:"__typename"`
	ID             int    `json:"id"`
	Title          *struct{ Russian, Original *string }
	ProductionYear *int `json:"productionYear"`
	ReleaseYears   []struct{ Start, End *int }
	Rating         *struct {
		Kinopoisk *struct{ Value *float64 } `json:"kinopoisk"`
		IMDb      *struct{ Value *float64 } `json:"imdb"`
	} `json:"rating"`
	Synopsis         *string `json:"synopsis"`
	ShortDescription *string `json:"shortDescription"`
	Genres           []struct{ Name string }
	Gallery          *struct {
		Posters *struct {
			KPVertical *struct {
				AvatarsURL string `json:"avatarsUrl"`
			} `json:"kpVertical"`
		} `json:"posters"`
	} `json:"gallery"`
}

var kpTypes = map[string]string{"Film": "FILM", "TvSeries": "TV_SERIES", "MiniSeries": "MINI_SERIES", "TvShow": "TV_SHOW", "Video": "VIDEO"}

func (m kpMovie) film() (Film, bool) {
	typ, ok := kpTypes[m.Typename]
	if !ok || m.ID == 0 {
		return Film{}, false
	}
	f := Film{ID: m.ID, Type: typ}
	if m.Title != nil {
		f.NameRu, f.NameOrig = deref(m.Title.Russian), deref(m.Title.Original)
	}
	if len(m.ReleaseYears) > 0 {
		f.Year, f.YearEnd = derefInt(m.ReleaseYears[0].Start), derefInt(m.ReleaseYears[0].End)
	}
	if f.Year == 0 {
		f.Year = derefInt(m.ProductionYear)
	}
	if m.Rating != nil {
		if m.Rating.Kinopoisk != nil {
			f.Rating = derefFloat(m.Rating.Kinopoisk.Value)
		}
		if m.Rating.IMDb != nil {
			f.RatingIMDb = derefFloat(m.Rating.IMDb.Value)
		}
	}
	return f, true
}

func (m kpMovie) details() (FilmDetails, bool) {
	f, ok := m.film()
	if !ok {
		return FilmDetails{}, false
	}
	d := FilmDetails{Film: f, Description: deref(m.Synopsis)}
	if d.Description == "" {
		d.Description = deref(m.ShortDescription)
	}
	for _, g := range m.Genres {
		d.Genres = append(d.Genres, g.Name)
	}
	if m.Gallery != nil && m.Gallery.Posters != nil && m.Gallery.Posters.KPVertical != nil {
		d.PosterURL = avatarsURL(m.Gallery.Posters.KPVertical.AvatarsURL)
	}
	return d, true
}

// avatarsURL — адрес постера нужного размера: у сайта «//avatars.mds.yandex.net/…» без размера.
func avatarsURL(u string) string {
	if u == "" {
		return ""
	}
	if strings.HasPrefix(u, "//") {
		u = "https:" + u
	}
	return strings.TrimRight(u, "/") + "/600x900"
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func derefInt(n *int) int {
	if n == nil {
		return 0
	}
	return *n
}

func derefFloat(f *float64) float64 {
	if f == nil {
		return 0
	}
	return *f
}

// Suggest — поиск сайта по названию (поле поиска kinopoisk.ru): лучший результат и до 5 фильмов;
// люди, кинотеатры и подборки пропускаются. Год в запрос не входит — он переставляет выдачу.
func (w *KPWeb) Suggest(ctx context.Context, class KPClass, keyword string) ([]Film, error) {
	var v struct {
		Suggest struct {
			Top struct {
				TopResult *struct {
					Global *kpMovie `json:"global"`
				} `json:"topResult"`
				Movies []struct {
					Movie kpMovie `json:"movie"`
				} `json:"movies"`
			} `json:"top"`
		} `json:"suggest"`
	}
	err := w.gql(ctx, class, opSuggest, map[string]any{"keyword": keyword, "yandexCityId": 213, "limit": 5, "withUserData": false}, &v)
	if errors.Is(err, errKPNotAllowed) {
		w.pauseOp(opSuggest)
	}
	if errors.Is(err, errKPNotAllowed) || errors.Is(err, errKPOpPaused) {
		return nil, ErrKPBlocked
	}
	if err != nil && !errors.Is(err, errKPMaybeNotFound) {
		return nil, err
	}
	var out []Film
	seen := map[int]bool{}
	add := func(m *kpMovie) {
		if m == nil {
			return
		}
		if f, ok := m.film(); ok && !seen[f.ID] {
			seen[f.ID] = true
			out = append(out, f)
		}
	}
	if v.Suggest.Top.TopResult != nil {
		add(v.Suggest.Top.TopResult.Global)
	}
	for i := range v.Suggest.Top.Movies {
		add(&v.Suggest.Top.Movies[i].Movie)
	}
	return out, nil
}

// Details — карточка по номеру: описание, жанры, рейтинги, постер. series — номер сериала
// (TvSeriesBaseInfo). Сайт не принимает запрос карточки — запасной путь: страница с JSON-LD.
func (w *KPWeb) Details(ctx context.Context, class KPClass, id int, series bool) (FilmDetails, error) {
	op, field := opFilm, "film"
	vars := map[string]any{"filmId": id, "isAuthorized": false, "actorsLimit": 10, "voiceOverActorsLimit": 5,
		"relatedMoviesLimit": 14, "catchupsLimit": 1, "includeSocialArgumentTypes": []string{}, "socialArgumentLimit": 0}
	if series {
		op, field = opSeries, "tvSeries"
		vars = map[string]any{"tvSeriesId": id, "isAuthorized": false, "withExternalViewOptions": false, "actorsLimit": 10,
			"voiceOverActorsLimit": 5, "relatedMoviesLimit": 14, "catchupsLimit": 1, "includeSocialArgumentTypes": []string{},
			"socialArgumentLimit": 0, "withPrechosenEpisode": false, "seasonNumber": 1, "episodeNumber": 1}
	}
	var v map[string]json.RawMessage // у ответа есть и webPage с другими полями — разбирается только нужное
	err := w.gql(ctx, class, op, vars, &v)
	switch {
	case errors.Is(err, errKPNotAllowed):
		w.pauseOp(op)
		return w.page(ctx, class, id, series)
	case errors.Is(err, errKPOpPaused):
		return w.page(ctx, class, id, series)
	case err != nil && !errors.Is(err, errKPMaybeNotFound):
		return FilmDetails{}, err
	}
	var m *kpMovie
	if raw, ok := v[field]; !ok || json.Unmarshal(raw, &m) != nil || m == nil {
		return FilmDetails{}, ErrNotFound
	}
	d, ok := m.details()
	if !ok {
		return FilmDetails{}, ErrNotFound
	}
	return d, nil
}

var reLDJSON = regexp.MustCompile(`(?s)<script[^>]*type="application/ld\+json"[^>]*>(.*?)</script>`)

// page — карточка со страницы фильма (данные в JSON-LD); cookie disable_server_sso_redirect=1
// убирает переход на вход Яндекса (исследование 22.4).
func (w *KPWeb) page(ctx context.Context, class KPClass, id int, series bool) (_ FilmDetails, err error) {
	if err := w.paused(""); err != nil {
		return FilmDetails{}, err
	}
	kind := "film"
	if series {
		kind = "series"
	}
	if err := w.acquire(ctx, class); err != nil {
		return FilmDetails{}, err
	}
	defer func() { err = w.settle(ctx, err) }()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, w.o.Site+"/"+kind+"/"+strconv.Itoa(id)+"/", nil)
	if err != nil {
		return FilmDetails{}, err
	}
	req.Header.Set("User-Agent", browserUA)
	req.Header.Set("Accept-Language", "ru-RU,ru;q=0.9")
	req.Header.Set("Cookie", "disable_server_sso_redirect=1")
	b, status, err := w.do(req)
	if err != nil {
		return FilmDetails{}, err
	}
	if status == http.StatusNotFound {
		return FilmDetails{}, ErrNotFound
	}
	for _, m := range reLDJSON.FindAllSubmatch(b, -1) {
		var ld struct {
			Type          string          `json:"@type"`
			Name          string          `json:"name"`
			AlternateName *string         `json:"alternateName"`
			DatePublished string          `json:"datePublished"`
			Genre         json.RawMessage `json:"genre"`
			Rating        *struct {
				Value json.Number `json:"ratingValue"`
			} `json:"aggregateRating"`
			Image       *string `json:"image"`
			Description string  `json:"description"`
		}
		if json.Unmarshal(m[1], &ld) != nil || (ld.Type != "Movie" && ld.Type != "TVSeries") {
			continue
		}
		d := FilmDetails{Film: Film{ID: id, NameRu: ld.Name, NameOrig: deref(ld.AlternateName), Type: "FILM"}, Description: ld.Description, PosterURL: deref(ld.Image)}
		if ld.Type == "TVSeries" {
			d.Type = "TV_SERIES"
		}
		if len(ld.DatePublished) >= 4 {
			d.Year, _ = strconv.Atoi(ld.DatePublished[:4])
		}
		if ld.Rating != nil {
			d.Rating, _ = ld.Rating.Value.Float64()
		}
		var genres []string
		if json.Unmarshal(ld.Genre, &genres) != nil {
			var one string
			if json.Unmarshal(ld.Genre, &one) == nil && one != "" {
				genres = []string{one}
			}
		}
		d.Genres = genres
		return d, nil
	}
	if reCaptcha.Match(b) {
		w.block("капча", w.o.Pause)
		return FilmDetails{}, ErrKPBlocked
	}
	return FilmDetails{}, kpTrouble{text: fmt.Sprintf("на странице нет данных фильма (ответ %d)", status)}
}
