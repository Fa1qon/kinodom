package meta

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/text/encoding/charmap"
	"golang.org/x/time/rate"
)

// Адреса Кинопоиска (спека, раздел 8; исследование, разделы 4, 11, 12). Кинопоиск — напрямую,
// не через прокси трекеров.
var (
	DefaultKinopoiskAPI = "https://kinopoiskapiunofficial.tech"
	DefaultRatingBase   = "https://rating.kinopoisk.ru"
)

var (
	ErrNoKey       = errors.New("Кинопоиск: не задан ключ — рейтинги только для раздач со ссылкой на Кинопоиск")
	ErrBadKey      = errors.New("Кинопоиск: ключ не подходит — проверьте его в настройках")
	ErrQuota       = errors.New("Кинопоиск: суточный лимит запросов исчерпан — рейтинги появятся завтра")
	ErrNotFound    = errors.New("Кинопоиск: фильм не найден")
	ErrRateLimited = errors.New("Кинопоиск: слишком частые запросы")
)

// ServiceError — Кинопоиск ответил 5xx. Поиск по названию на кириллице бывает отвечает 500 — не
// на каждое название (исследование, разделы 12–13), а каждый такой ответ тратит квоту.
type ServiceError struct {
	Status int
}

func (e *ServiceError) Error() string {
	return fmt.Sprintf("Кинопоиск: сбой сервиса (ответ %d)", e.Status)
}

type KinopoiskOptions struct {
	Key        string        // ключ из настроек при старте; "" — только пути без ключа; дальше — SetKey
	APIBase    string        // "" — DefaultKinopoiskAPI
	RatingBase string        // "" — DefaultRatingBase
	Rate       rate.Limit    // 0 — 3 запроса/с (спека, раздел 5: заявлено 5, берём с запасом)
	Timeout    time.Duration // 0 — 30 с
}

// Kinopoisk — клиент kinopoiskapiunofficial.tech и запасных путей без ключа. Безопасен для
// одновременного использования; все запросы — через один ограничитель.
type Kinopoisk struct {
	o    KinopoiskOptions
	http *http.Client
	lim  *rate.Limiter

	mu  sync.Mutex // ключ меняется в настройках на ходу (SetKey)
	key string
}

func NewKinopoisk(o KinopoiskOptions) *Kinopoisk {
	if o.APIBase == "" {
		o.APIBase = DefaultKinopoiskAPI
	}
	if o.RatingBase == "" {
		o.RatingBase = DefaultRatingBase
	}
	o.APIBase = strings.TrimRight(o.APIBase, "/")
	o.RatingBase = strings.TrimRight(o.RatingBase, "/")
	if o.Rate == 0 {
		o.Rate = 3
	}
	if o.Timeout == 0 {
		o.Timeout = 30 * time.Second
	}
	return &Kinopoisk{o: o, http: &http.Client{Timeout: o.Timeout}, lim: rate.NewLimiter(o.Rate, 1), key: o.Key}
}

// SetKey — новый ключ из настроек: действует со следующего запроса, без перезапуска (хвост 5b).
func (k *Kinopoisk) SetKey(key string) {
	k.mu.Lock()
	k.key = key
	k.mu.Unlock()
}

func (k *Kinopoisk) currentKey() string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.key
}

// HasKey — ключ задан.
func (k *Kinopoisk) HasKey() bool { return k.currentKey() != "" }

// Film — фильм или сериал Кинопоиска.
type Film struct {
	ID         int
	IMDbID     string
	NameRu     string
	NameOrig   string // nameOriginal, иначе nameEn
	Year       int    // 0 — неизвестен
	Type       string // FILM, TV_SERIES, MINI_SERIES, TV_SHOW, VIDEO
	Rating     float64
	RatingIMDb float64 // 0 — рейтинга нет (фильм не вышел или мало оценок)
}

// Quota — лимиты ключа. TotalLimit = -1 — общего лимита нет.
type Quota struct {
	DailyLimit, DailyUsed int
	TotalLimit, TotalUsed int
	Account               string
}

// Quota — лимиты ключа (/api/v1/api_keys/{key}). Сам этот запрос квоту не тратит.
func (k *Kinopoisk) Quota(ctx context.Context) (Quota, error) {
	var v struct {
		TotalQuota struct{ Value, Used int } `json:"totalQuota"`
		DailyQuota struct{ Value, Used int } `json:"dailyQuota"`
		Account    string                    `json:"accountType"`
	}
	key := k.currentKey()
	if key == "" {
		return Quota{}, ErrNoKey
	}
	if err := k.getJSON(ctx, "лимиты ключа", "/api/v1/api_keys/"+url.PathEscape(key), nil, &v); err != nil {
		return Quota{}, err
	}
	return Quota{DailyLimit: v.DailyQuota.Value, DailyUsed: v.DailyQuota.Used,
		TotalLimit: v.TotalQuota.Value, TotalUsed: v.TotalQuota.Used, Account: v.Account}, nil
}

// Film — фильм по номеру (/api/v2.2/films/{id}).
func (k *Kinopoisk) Film(ctx context.Context, id int) (Film, error) {
	var v apiFilm
	if err := k.getJSON(ctx, "фильм "+strconv.Itoa(id), "/api/v2.2/films/"+strconv.Itoa(id), nil, &v); err != nil {
		return Film{}, err
	}
	return v.film(), nil
}

// FilmDetails — фильм с описанием, жанрами и постером: карточки медиатеки (спека этапа 9, раздел 5.4).
type FilmDetails struct {
	Film
	Description string
	Genres      []string
	PosterURL   string // "" — постера нет
}

// Details — фильм по номеру с описанием, жанрами и постером (/api/v2.2/films/{id}).
func (k *Kinopoisk) Details(ctx context.Context, id int) (FilmDetails, error) {
	var v struct {
		apiFilm
		Description *string `json:"description"`
		PosterURL   *string `json:"posterUrl"`
		Genres      []struct {
			Genre string `json:"genre"`
		} `json:"genres"`
	}
	if err := k.getJSON(ctx, "фильм "+strconv.Itoa(id), "/api/v2.2/films/"+strconv.Itoa(id), nil, &v); err != nil {
		return FilmDetails{}, err
	}
	d := FilmDetails{Film: v.film(), Description: str(v.Description), PosterURL: str(v.PosterURL), Genres: []string{}}
	for _, g := range v.Genres {
		if g.Genre != "" {
			d.Genres = append(d.Genres, g.Genre)
		}
	}
	return d, nil
}

// ByIMDb — фильм по номеру IMDb (/api/v2.2/films?imdbId=): точное совпадение одним запросом.
func (k *Kinopoisk) ByIMDb(ctx context.Context, imdbID string) (Film, error) {
	fs, err := k.films(ctx, "поиск по IMDb "+imdbID, url.Values{"imdbId": {imdbID}})
	if err != nil {
		return Film{}, err
	}
	if len(fs) == 0 {
		return Film{}, ErrNotFound
	}
	return fs[0], nil
}

// Search — поиск по названию и году ±1 (/api/v2.2/films). year = 0 — без года. Порядок — по
// числу оценок: известный фильм первым. На кириллице бывает *ServiceError (500).
func (k *Kinopoisk) Search(ctx context.Context, keyword string, year int) ([]Film, error) {
	q := url.Values{"keyword": {keyword}, "order": {"NUM_VOTE"}, "type": {"ALL"}}
	if year > 0 {
		q.Set("yearFrom", strconv.Itoa(year-1))
		q.Set("yearTo", strconv.Itoa(year+1))
	}
	return k.films(ctx, "поиск «"+keyword+"»", q)
}

func (k *Kinopoisk) films(ctx context.Context, what string, q url.Values) ([]Film, error) {
	var v struct {
		Items []apiFilm `json:"items"`
	}
	if err := k.getJSON(ctx, what, "/api/v2.2/films", q, &v); err != nil {
		return nil, err
	}
	out := make([]Film, 0, len(v.Items))
	for _, it := range v.Items {
		out = append(out, it.film())
	}
	return out, nil
}

// KeylessRating — рейтинги по номеру без ключа и без квоты (rating.kinopoisk.ru/{id}.xml,
// недокументировано): когда квота исчерпана или ключа нет.
func (k *Kinopoisk) KeylessRating(ctx context.Context, id int) (kp, imdb float64, err error) {
	body, err := k.get(ctx, "рейтинг "+strconv.Itoa(id), k.o.RatingBase+"/"+strconv.Itoa(id)+".xml", false)
	if err != nil {
		return 0, 0, err
	}
	var v struct {
		KP   string `xml:"kp_rating"`
		IMDb string `xml:"imdb_rating"`
	}
	dec := xml.NewDecoder(bytes.NewReader(body))
	dec.CharsetReader = func(label string, in io.Reader) (io.Reader, error) {
		if strings.EqualFold(label, "windows-1251") {
			return charmap.Windows1251.NewDecoder().Reader(in), nil
		}
		return nil, fmt.Errorf("кодировка %q", label)
	}
	if err := dec.Decode(&v); err != nil {
		return 0, 0, fmt.Errorf("Кинопоиск: рейтинг %d не читается: %w", id, err)
	}
	kp, _ = strconv.ParseFloat(strings.TrimSpace(v.KP), 64)
	imdb, _ = strconv.ParseFloat(strings.TrimSpace(v.IMDb), 64)
	return kp, imdb, nil
}

// PosterURL — постер Кинопоиска без ключа и без квоты. Нет постера — редирект на no-poster.gif.
func (k *Kinopoisk) PosterURL(id int) string {
	return k.o.APIBase + "/images/posters/kp/" + strconv.Itoa(id) + ".jpg"
}

func (k *Kinopoisk) getJSON(ctx context.Context, what, path string, q url.Values, v any) error {
	if !k.HasKey() {
		return ErrNoKey
	}
	u := k.o.APIBase + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	body, err := k.get(ctx, what, u, true)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("Кинопоиск: %s — ответ не читается: %w", what, err)
	}
	return nil
}

// get — один запрос. what — что спрашивали, для текста ошибки: адрес в текст не попадает,
// в нём бывает ключ (/api/v1/api_keys/{key}).
func (k *Kinopoisk) get(ctx context.Context, what, u string, withKey bool) ([]byte, error) {
	if err := k.lim.Wait(ctx); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("Кинопоиск: очередь запросов не успевает до срока: %w", context.DeadlineExceeded)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("Кинопоиск: %s — неверный запрос", what)
	}
	if withKey {
		req.Header.Set("X-API-KEY", k.currentKey())
		req.Header.Set("Accept", "application/json")
	}
	resp, err := k.http.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // без адреса
		}
		return nil, fmt.Errorf("Кинопоиск: %s — %w", what, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("Кинопоиск: %s — ответ оборвался: %w", what, err)
	}
	switch {
	case resp.StatusCode == http.StatusOK:
		return body, nil
	case resp.StatusCode == http.StatusUnauthorized:
		return nil, ErrBadKey
	case resp.StatusCode == http.StatusPaymentRequired:
		return nil, ErrQuota
	case resp.StatusCode == http.StatusNotFound:
		return nil, ErrNotFound
	case resp.StatusCode == http.StatusTooManyRequests:
		return nil, ErrRateLimited
	case resp.StatusCode >= 500:
		return nil, &ServiceError{Status: resp.StatusCode}
	}
	return nil, fmt.Errorf("Кинопоиск: %s — ответ %d", what, resp.StatusCode)
}

// apiFilm — фильм в ответах /films и /films/{id}. Почти все поля бывают null.
type apiFilm struct {
	KinopoiskID     int      `json:"kinopoiskId"`
	IMDbID          *string  `json:"imdbId"`
	NameRu          *string  `json:"nameRu"`
	NameEn          *string  `json:"nameEn"`
	NameOriginal    *string  `json:"nameOriginal"`
	Year            flexInt  `json:"year"`
	Type            string   `json:"type"`
	RatingKinopoisk *float64 `json:"ratingKinopoisk"`
	RatingImdb      *float64 `json:"ratingImdb"`
}

func (a apiFilm) film() Film {
	f := Film{ID: a.KinopoiskID, IMDbID: str(a.IMDbID), NameRu: str(a.NameRu), NameOrig: str(a.NameOriginal),
		Year: int(a.Year), Type: a.Type}
	if f.NameOrig == "" {
		f.NameOrig = str(a.NameEn)
	}
	if a.RatingKinopoisk != nil {
		f.Rating = *a.RatingKinopoisk
	}
	if a.RatingImdb != nil {
		f.RatingIMDb = *a.RatingImdb
	}
	return f
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// flexInt — год приходит числом, строкой («2012» в старых ответах) или null.
type flexInt int

func (f *flexInt) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "null" || s == "" {
		*f = 0
		return nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		*f = 0 // «2012-2014» и прочее — год неизвестен, а не ошибка разбора всего ответа
		return nil
	}
	*f = flexInt(n)
	return nil
}
