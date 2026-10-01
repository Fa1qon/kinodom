package meta

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"time"

	"golang.org/x/text/encoding/charmap"
)

// Списки Кинопоиска без ключа (план 14Г): запрос сайта MovieDesktopListPage — текст снят с чанка страницы
// /lists/movies/ (graphql-js print), сайт принимает его байт в байт (исследование 2026-10-01). Оценка IMDb в
// списке не приходит — её отдаёт rating.kinopoisk.ru/{id}.xml.

// DefaultKPRating — сервис оценок Кинопоиска (КП и IMDb одного фильма, XML в cp1251).
var DefaultKPRating = "https://rating.kinopoisk.ru"

// opList — запрос списка сайта.
const opList = "MovieDesktopListPage"

// ListQuery — список Кинопоиска.
type ListQuery struct {
	Slug   string   // «popular-films», «popular-series»; "" — все фильмы
	Bool   []string // логические фильтры: russian, foreign, released, films, series…
	Genre  string   // слаг жанра (documentary); "" — без
	Order  string   // POSITION_ASC (порядок списка — популярность), KP_RATING_DESC, VOTES_COUNT_DESC, YEAR_DESC…
	Limit  int      // не больше 50
	Offset int
}

// ListFilm — фильм списка.
type ListFilm struct {
	ID        int
	Type      string // FILM, TV_SERIES, MINI_SERIES, TV_SHOW, VIDEO
	NameRu    string
	NameOrig  string
	Year      int
	Rating    float64 // оценка Кинопоиска; 0 — нет
	Votes     int
	Premiere  time.Time // мировая премьера, иначе выход в России, иначе первый выход; нуль — неизвестна
	Poster    string    // https://avatars…/300x450; "" — нет
	Genres    []string
	Countries []string
}

// listItemTypes — виды элементов списка, которые принимает пульт сайта.
var listItemTypes = []string{"COMING_SOON_MOVIE_LIST_ITEM", "MOVIE_LIST_ITEM", "TOP_MOVIE_LIST_ITEM", "POPULAR_MOVIE_LIST_ITEM",
	"MOST_PROFITABLE_MOVIE_LIST_ITEM", "MOST_EXPENSIVE_MOVIE_LIST_ITEM", "BOX_OFFICE_MOVIE_LIST_ITEM",
	"OFFLINE_AUDIENCE_MOVIE_LIST_ITEM", "RECOMMENDATION_MOVIE_LIST_ITEM", "MOVIE_IN_CINEMA_LIST_ITEM", "PLANNED_TO_WATCH_LIST_ITEM"}

type kpDate struct {
	Date *struct {
		Date string `json:"date"`
	} `json:"date"`
}

type kpListMovie struct {
	Typename       string `json:"__typename"`
	ID             int    `json:"id"`
	Title          *struct{ Russian, Original *string }
	ProductionYear *int `json:"productionYear"`
	ReleaseYears   []struct{ Start, End *int }
	Rating         *struct {
		Kinopoisk *struct {
			Value *float64 `json:"value"`
			Count *int     `json:"count"`
		} `json:"kinopoisk"`
	} `json:"rating"`
	Gallery *struct {
		Posters *struct {
			Vertical *struct {
				AvatarsURL string `json:"avatarsUrl"`
			} `json:"vertical"`
		} `json:"posters"`
	} `json:"gallery"`
	Genres       []struct{ Name string }
	Countries    []struct{ Name string }
	Distribution *struct {
		WorldPremiere *struct {
			IncompleteDate *struct {
				Date string `json:"date"`
			} `json:"incompleteDate"`
		} `json:"worldPremiere"`
		RusRelease  *struct{ Items []kpDate } `json:"rusRelease"`
		AllReleases *struct{ Items []kpDate } `json:"allReleases"`
	} `json:"distribution"`
}

// List — страница списка Кинопоиска (не больше 50 фильмов) и сколько в списке всего.
func (w *KPWeb) List(ctx context.Context, class KPClass, q ListQuery) (items []ListFilm, total int, err error) {
	bools := slices.Clone(q.Bool)
	slices.Sort(bools) // сайт шлёт логические фильтры по алфавиту
	bv := make([]map[string]any, 0, len(bools))
	for _, b := range bools {
		bv = append(bv, map[string]any{"filterId": b, "value": true})
	}
	sv := []map[string]any{}
	if q.Genre != "" {
		sv = append(sv, map[string]any{"filterId": "genre", "value": q.Genre})
	}
	vars := map[string]any{
		"slug": q.Slug, "platform": "DESKTOP", "withUserData": false,
		"supportedFilterTypes": []string{"BOOLEAN", "SINGLE_SELECT"},
		"filters": map[string]any{"booleanFilterValues": bv, "singleSelectFilterValues": sv,
			"intRangeFilterValues": []any{}, "multiSelectFilterValues": []any{}, "realRangeFilterValues": []any{}},
		"singleSelectFiltersLimit": 1, "singleSelectFiltersOffset": 0,
		"moviesLimit": q.Limit, "moviesOffset": q.Offset, "moviesOrder": q.Order,
		"supportedItemTypes": listItemTypes,
	}
	var out struct {
		MovieListBySlug *struct {
			Movies struct {
				Total int `json:"total"`
				Items []struct {
					Movie *kpListMovie `json:"movie"`
				} `json:"items"`
			} `json:"movies"`
		} `json:"movieListBySlug"`
	}
	err = w.gql(ctx, class, opList, vars, &out)
	if errors.Is(err, errKPNotAllowed) {
		w.pauseOp(opList) // сайт обновил сборку — не спрашивать раньше паузы (ревью 14Г)
	}
	if errors.Is(err, errKPNotAllowed) || errors.Is(err, errKPOpPaused) {
		return nil, 0, ErrKPBlocked
	}
	if err != nil {
		return nil, 0, err
	}
	if out.MovieListBySlug == nil {
		return nil, 0, kpTrouble{text: "пустой список"}
	}
	for _, it := range out.MovieListBySlug.Movies.Items {
		if f, ok := listFilm(it.Movie); ok {
			items = append(items, f)
		}
	}
	return items, out.MovieListBySlug.Movies.Total, nil
}

func listFilm(m *kpListMovie) (ListFilm, bool) {
	if m == nil {
		return ListFilm{}, false
	}
	typ, ok := kpTypes[m.Typename]
	if !ok || m.ID == 0 {
		return ListFilm{}, false
	}
	f := ListFilm{ID: m.ID, Type: typ}
	if m.Title != nil {
		f.NameRu, f.NameOrig = deref(m.Title.Russian), deref(m.Title.Original)
	}
	if len(m.ReleaseYears) > 0 {
		f.Year = derefInt(m.ReleaseYears[0].Start)
	}
	if f.Year == 0 {
		f.Year = derefInt(m.ProductionYear)
	}
	if m.Rating != nil && m.Rating.Kinopoisk != nil {
		f.Rating, f.Votes = derefFloat(m.Rating.Kinopoisk.Value), derefInt(m.Rating.Kinopoisk.Count)
	}
	if m.Gallery != nil && m.Gallery.Posters != nil && m.Gallery.Posters.Vertical != nil && m.Gallery.Posters.Vertical.AvatarsURL != "" {
		u := m.Gallery.Posters.Vertical.AvatarsURL
		if len(u) > 1 && u[:2] == "//" {
			u = "https:" + u
		}
		f.Poster = u + "/300x450"
	}
	for _, g := range m.Genres {
		f.Genres = append(f.Genres, g.Name)
	}
	for _, c := range m.Countries {
		f.Countries = append(f.Countries, c.Name)
	}
	if d := m.Distribution; d != nil {
		var dates []string
		if d.WorldPremiere != nil && d.WorldPremiere.IncompleteDate != nil {
			dates = append(dates, d.WorldPremiere.IncompleteDate.Date)
		}
		for _, l := range []*struct{ Items []kpDate }{d.RusRelease, d.AllReleases} {
			if l != nil && len(l.Items) > 0 && l.Items[0].Date != nil {
				dates = append(dates, l.Items[0].Date.Date)
			}
		}
		for _, s := range dates {
			if t, err := time.Parse("2006-01-02", s); err == nil {
				f.Premiere = t
				break
			}
		}
	}
	return f, true
}

var reIMDbRating = regexp.MustCompile(`imdb_rating[^>]*num_vote="(\d+)"[^>]*>([\d.]+)<`)

// IMDb — оценка IMDb фильма Кинопоиска (rating.kinopoisk.ru/{id}.xml): свои ворота (раз в RatingEvery),
// отказ — пауза только оценкам. У фильма без IMDb — 0, 0 без ошибки.
func (w *KPWeb) IMDb(ctx context.Context, id int) (rating float64, votes int, err error) {
	if err := w.ratingGate(ctx); err != nil {
		return 0, 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, w.o.RatingBase+"/"+strconv.Itoa(id)+".xml", nil)
	if err != nil {
		return 0, 0, err
	}
	resp, err := w.ratingClient.Do(req)
	if err != nil {
		return 0, 0, kpTrouble{text: "оценки IMDb не отвечают"}
	}
	defer resp.Body.Close()
	block := func() (float64, int, error) {
		w.mu.Lock()
		w.ratingBlocked = w.o.Now().Add(w.o.Pause)
		w.mu.Unlock()
		return 0, 0, ErrKPBlocked
	}
	switch {
	case resp.StatusCode == http.StatusOK:
	case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests ||
		resp.StatusCode >= 300 && resp.StatusCode < 400: // переход — на капчу (ревью 14Г)
		return block()
	default:
		return 0, 0, kpTrouble{text: fmt.Sprintf("оценки IMDb: ответ %d", resp.StatusCode)}
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return 0, 0, kpTrouble{text: "оценки IMDb: ответ оборвался"}
	}
	if u, err := charmap.Windows1251.NewDecoder().Bytes(b); err == nil {
		b = u
	}
	if !bytes.Contains(b, []byte("<rating")) && !bytes.Contains(b, []byte("kp_rating")) {
		return block() // не оценки, а страница (капча) — не «у фильма нет IMDb»
	}
	m := reIMDbRating.FindSubmatch(b)
	if m == nil {
		return 0, 0, nil
	}
	votes, _ = strconv.Atoi(string(m[1]))
	rating, _ = strconv.ParseFloat(string(m[2]), 64)
	return rating, votes, nil
}

// ratingGate — ворота оценок: не чаще RatingEvery, после отказа — пауза.
func (w *KPWeb) ratingGate(ctx context.Context) error {
	w.mu.Lock()
	now := w.o.Now()
	if now.Before(w.ratingBlocked) {
		w.mu.Unlock()
		return ErrKPBlocked
	}
	wait := time.Until(w.ratingNext)
	if w.ratingNext.Before(time.Now()) {
		w.ratingNext = time.Now()
		wait = 0
	}
	w.ratingNext = w.ratingNext.Add(w.o.RatingEvery)
	w.mu.Unlock()
	if wait <= 0 {
		return nil
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
