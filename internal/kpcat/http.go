package kpcat

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"kinodom/internal/httpx"
)

// PageSize — фильмов в порции пульта (как у каталога трекеров).
const PageSize = 24

// Router — то, что модулю нужно от HTTP-сервера; api.Server ему соответствует.
type Router interface {
	Handle(pattern, module string, h http.Handler)
}

// FilmView — фильм каталога для пульта.
type FilmView struct {
	ID        int        `json:"id"`
	Type      string     `json:"type"` // FILM, TV_SERIES, MINI_SERIES, TV_SHOW, VIDEO
	Title     string     `json:"title"`
	Original  string     `json:"original"`
	Year      int        `json:"year"`
	Kinopoisk float64    `json:"kinopoisk"` // 0 — оценки нет
	IMDb      float64    `json:"imdb"`      // 0 — нет у сайта или ещё не пришла
	Votes     int        `json:"votes"`
	Premiere  *time.Time `json:"premiere,omitempty"`
	Poster    string     `json:"poster"` // адрес постера у сервера; "" — нет
	Genres    []string   `json:"genres"`
	Countries []string   `json:"countries"`
}

// ListView — порция раздела.
type ListView struct {
	Entries []FilmView `json:"entries"`
	Total   int        `json:"total"`
	More    bool       `json:"more"`
}

type SectionView struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Count int    `json:"count"`
}

type OrderView struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// CatalogView — разделы, порядки и последнее обновление.
type CatalogView struct {
	Sections  []SectionView `json:"sections"`
	Orders    []OrderView   `json:"orders"`
	UpdatedAt *time.Time    `json:"updatedAt"`
}

func (m *Module) Register(r Router) {
	n := m.Name()
	r.Handle("GET /api/v1/kpcat", n, http.HandlerFunc(m.handleCatalog))
	r.Handle("GET /api/v1/kpcat/list", n, http.HandlerFunc(m.handleList))
	r.Handle("GET /api/v1/kpcat/films/{id}", n, http.HandlerFunc(m.handleFilm))
	r.Handle("GET /api/v1/kpcat/films/{id}/poster", n, http.HandlerFunc(m.handlePoster))
}

func (m *Module) handleCatalog(w http.ResponseWriter, r *http.Request) {
	counts, at, err := m.st.counts(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "каталог Кинопоиска не читается: "+err.Error())
		return
	}
	out := CatalogView{Sections: []SectionView{}, Orders: []OrderView{}}
	for _, s := range sections {
		out.Sections = append(out.Sections, SectionView{ID: s.id, Name: s.name, Count: counts[s.id]})
	}
	for _, o := range orders {
		out.Orders = append(out.Orders, OrderView{ID: o.id, Name: o.name})
	}
	if !at.IsZero() {
		out.UpdatedAt = &at
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

func (m *Module) handleList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	sec := q.Get("section")
	if !known(sec) {
		httpx.WriteError(w, http.StatusBadRequest, "такого раздела нет")
		return
	}
	offset, _ := strconv.Atoi(q.Get("offset"))
	offset = max(offset, 0)
	limit, err := strconv.Atoi(q.Get("limit"))
	if err != nil || limit <= 0 || limit > 100 {
		limit = PageSize
	}
	es, total, err := m.list(r.Context(), sec, q.Get("order"), offset, limit)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "каталог Кинопоиска не читается: "+err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, ListView{Entries: es, Total: total, More: offset+len(es) < total})
}

func (m *Module) filmOf(w http.ResponseWriter, r *http.Request) (film, bool) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "неверный номер фильма")
		return film{}, false
	}
	f, err := m.st.film(r.Context(), id)
	switch {
	case errors.Is(err, errNoFilm):
		httpx.WriteError(w, http.StatusNotFound, err.Error())
		return film{}, false
	case err != nil:
		httpx.WriteError(w, http.StatusInternalServerError, "каталог Кинопоиска не читается: "+err.Error())
		return film{}, false
	}
	return f, true
}

func (m *Module) handleFilm(w http.ResponseWriter, r *http.Request) {
	if f, ok := m.filmOf(w, r); ok {
		httpx.WriteJSON(w, http.StatusOK, f.view())
	}
}

// handlePoster — постер фильма: в кэш картинок по адресу аватара Кинопоиска, ответ — переход на /img/{key}.
func (m *Module) handlePoster(w http.ResponseWriter, r *http.Request) {
	f, ok := m.filmOf(w, r)
	if !ok {
		return
	}
	if f.Poster == "" || m.o.Posters == nil {
		httpx.WriteError(w, http.StatusNotFound, "постера нет")
		return
	}
	key, err := m.o.Posters(r.Context(), f.Poster)
	if err != nil {
		httpx.WriteError(w, http.StatusBadGateway, "постер не скачался: "+err.Error())
		return
	}
	w.Header().Set("Cache-Control", "max-age=86400")
	http.Redirect(w, r, "/img/"+key, http.StatusFound)
}
