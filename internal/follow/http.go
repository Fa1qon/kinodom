package follow

import (
	"errors"
	"net/http"
	"strconv"

	"kinodom/internal/catalog"
	"kinodom/internal/httpx"
)

// Router — маршруты API: изменения — только из домашней сети (HandleHome).
type Router interface {
	Handle(pattern, module string, h http.Handler)
	HandleHome(pattern, module string, h http.Handler)
}

// Register — «Следить» и «Не следить» у раздачи, «Новые серии» и «Убрать» (спека 11b, 6.6).
func (m *Module) Register(r Router) {
	r.HandleHome("PUT /api/v1/releases/{id}/follow", m.Name(), http.HandlerFunc(m.handleFollow))
	r.HandleHome("DELETE /api/v1/releases/{id}/follow", m.Name(), http.HandlerFunc(m.handleUnfollow))
	r.Handle("GET /api/v1/updates", m.Name(), http.HandlerFunc(m.handleUpdates))
	r.HandleHome("DELETE /api/v1/updates/{id}", m.Name(), http.HandlerFunc(m.handleDismiss))
}

func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		httpx.WriteError(w, http.StatusBadRequest, "номер — целое положительное число")
		return 0, false
	}
	return id, true
}

func (m *Module) handleFollow(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	switch err := m.Follow(r.Context(), id); {
	case errors.Is(err, catalog.ErrNoRelease):
		httpx.WriteError(w, http.StatusNotFound, "Такой раздачи нет")
	case errors.Is(err, ErrNotSeries):
		httpx.WriteError(w, http.StatusConflict, "Это не сериал")
	case err != nil:
		httpx.WriteError(w, http.StatusInternalServerError, "Подписка не сохранилась: "+err.Error())
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

func (m *Module) handleUnfollow(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := m.Unfollow(r.Context(), id); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Подписка не снялась: "+err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (m *Module) handleUpdates(w http.ResponseWriter, r *http.Request) {
	us, err := m.Updates(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Новые серии не читаются: "+err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, us)
}

func (m *Module) handleDismiss(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := m.Dismiss(r.Context(), id); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Строка не убралась: "+err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
