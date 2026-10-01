package library

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strconv"

	"kinodom/internal/httpx"
)

// Router — где регистрируются маршруты (api.Server): чтение — с любого устройства, изменения — из
// домашней сети (спека этапа 7, раздел 10.1).
type Router interface {
	Handle(pattern, module string, h http.Handler)
	HandleHome(pattern, module string, h http.Handler)
}

// Register — маршруты медиатеки (спека, раздел 5.10).
func (l *Library) Register(r Router) {
	n := l.Name()
	r.Handle("GET /api/v1/library", n, http.HandlerFunc(l.handleList))
	r.Handle("GET /api/v1/library/cards/{key}", n, http.HandlerFunc(l.handleCard))
	r.HandleHome("PUT /api/v1/library/cards/{key}", n, http.HandlerFunc(l.handleCardPut))
	r.Handle("POST /api/v1/library/scan", n, http.HandlerFunc(l.handleScan))
	r.Handle("GET /api/v1/library/unrecognized", n, http.HandlerFunc(l.handleUnrecognized))
	r.HandleHome("PUT /api/v1/library/units/{id}", n, http.HandlerFunc(l.handleUnitPut))
	r.HandleHome("DELETE /api/v1/library/units/{id}", n, http.HandlerFunc(l.handleUnitDelete))
	r.Handle("GET /api/v1/library/units/{id}/poster", n, http.HandlerFunc(l.handlePoster))
	r.Handle("GET /api/v1/library/categories", n, http.HandlerFunc(l.handleCategories))
	r.HandleHome("POST /api/v1/library/categories", n, http.HandlerFunc(l.handleCategoryAdd))
	r.HandleHome("PUT /api/v1/library/categories/{id}", n, http.HandlerFunc(l.handleCategoryPut))
	r.HandleHome("DELETE /api/v1/library/categories/{id}", n, http.HandlerFunc(l.handleCategoryDelete))
	r.HandleHome("PUT /api/v1/library/categories/{id}/device", n, http.HandlerFunc(l.handleDevice))
	r.HandleHome("DELETE /api/v1/library/categories/{id}/device", n, http.HandlerFunc(l.handleDevice))
	r.Handle("GET /api/v1/library/files/{file}/play", n, http.HandlerFunc(l.handlePlay))
	r.Handle("GET /media/{file}/{name}", n, http.HandlerFunc(l.handleMedia))
	r.Handle("GET /m3u/library/{file}", n, http.HandlerFunc(l.handleM3U))
}

// writeError — ответ на ошибку медиатеки: неверный ввод — 400, нет — 404, конфликт — 409.
func writeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNoCard), errors.Is(err, ErrNoUnit), errors.Is(err, ErrNoCategory), errors.Is(err, errNoFile):
		httpx.WriteError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, ErrNoWrite):
		httpx.WriteError(w, http.StatusForbidden, err.Error())
	case errors.Is(err, ErrBuiltin), errors.Is(err, ErrFolderTaken), errors.Is(err, ErrTorrentUnit), errors.Is(err, ErrOutside):
		httpx.WriteError(w, http.StatusConflict, err.Error())
	case errors.Is(err, ErrBadKP), errors.Is(err, ErrNoTitle), errors.Is(err, ErrNoName), errors.Is(err, ErrBadLayout),
		errors.Is(err, ErrNotLocal), errors.Is(err, ErrFolderMissing), errors.Is(err, ErrFolderAccess), errors.Is(err, ErrFolderInDownloads):
		httpx.WriteError(w, http.StatusBadRequest, err.Error())
	default:
		httpx.WriteError(w, http.StatusInternalServerError, "медиатека: "+err.Error())
	}
}

func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		httpx.WriteError(w, http.StatusBadRequest, "неверный номер")
		return 0, false
	}
	return id, true
}

func (l *Library) handleList(w http.ResponseWriter, r *http.Request) {
	var cat int64
	if s := r.URL.Query().Get("category"); s != "" {
		var err error
		if cat, err = strconv.ParseInt(s, 10, 64); err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "неверная категория")
			return
		}
	}
	v, err := l.List(r.Context(), httpx.Device(r), cat)
	if err != nil {
		writeError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, v)
}

func (l *Library) handleCard(w http.ResponseWriter, r *http.Request) {
	c, err := l.Card(r.Context(), httpx.Device(r), r.PathValue("key"))
	if err != nil {
		writeError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, c)
}

// handleCardPut — «Перенести в категорию»: {category: <id>} или {category: null} — вернуть.
func (l *Library) handleCardPut(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Category json.RawMessage `json:"category"`
	}
	if !httpx.ReadJSON(w, r, &req) {
		return
	}
	var cat *int64
	switch string(req.Category) {
	case "":
		httpx.WriteError(w, http.StatusBadRequest, "нужна категория (category) — номер или null")
		return
	case "null":
	default:
		var id int64
		if err := json.Unmarshal(req.Category, &id); err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "категория — номер или null")
			return
		}
		cat = &id
	}
	if err := l.SetCardCategory(r.Context(), r.PathValue("key"), cat); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleScan — пульт открыл медиатеку: обход в фоне (не чаще раза в минуту).
func (l *Library) handleScan(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, l.Scan())
}

func (l *Library) handleUnrecognized(w http.ResponseWriter, r *http.Request) {
	out, err := l.Unrecognized(r.Context(), httpx.Device(r))
	if err != nil {
		writeError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// handleUnitDelete — «Удалить» свою единицу медиатеки (план 14В): {deleted: true}.
func (l *Library) handleUnitDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := l.DeleteUnit(r.Context(), id); err != nil {
		writeError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

// handleUnitPut — правка единицы: {kinopoisk: "<ссылка или номер>"}, {manual: {title, year}},
// {search: true}, {reset: true}.
func (l *Library) handleUnitPut(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		Kinopoisk *string `json:"kinopoisk"`
		Manual    *struct {
			Title string `json:"title"`
			Year  int    `json:"year"`
		} `json:"manual"`
		Search bool `json:"search"`
		Reset  bool `json:"reset"`
	}
	if !httpx.ReadJSON(w, r, &req) {
		return
	}
	var err error
	switch {
	case req.Kinopoisk != nil:
		err = l.LinkKP(r.Context(), id, *req.Kinopoisk)
	case req.Manual != nil:
		err = l.MarkManual(r.Context(), id, req.Manual.Title, req.Manual.Year)
	case req.Search:
		err = l.SearchAgain(r.Context(), id)
	case req.Reset:
		err = l.ResetUnit(r.Context(), id)
	default:
		httpx.WriteError(w, http.StatusBadRequest, "нужно одно из: kinopoisk, manual, search, reset")
		return
	}
	if err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handlePoster — постер из папки единицы (poster.jpg, folder.jpg, cover.jpg).
func (l *Library) handlePoster(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	p := l.LocalPoster(r.Context(), id)
	if p == "" {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(p)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "max-age=3600")
	http.ServeContent(w, r, p, fi.ModTime(), f)
}

func (l *Library) handleCategories(w http.ResponseWriter, r *http.Request) {
	cs, err := l.Categories(r.Context(), httpx.Device(r))
	if err != nil {
		writeError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, cs)
}

func (l *Library) handleCategoryAdd(w http.ResponseWriter, r *http.Request) {
	var in CategoryInput
	if !httpx.ReadJSON(w, r, &in) {
		return
	}
	id, err := l.AddCategory(r.Context(), in)
	if err != nil {
		writeError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, map[string]int64{"id": id})
}

func (l *Library) handleCategoryPut(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in CategoryInput
	if !httpx.ReadJSON(w, r, &in) {
		return
	}
	if err := l.UpdateCategory(r.Context(), id, in); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (l *Library) handleCategoryDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := l.DeleteCategory(r.Context(), id); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleDevice — «Показывать на этом устройстве» у скрытой категории: PUT — да, DELETE — нет.
func (l *Library) handleDevice(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := l.SetDeviceCategory(r.Context(), httpx.Device(r), id, r.Method == http.MethodPut); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
