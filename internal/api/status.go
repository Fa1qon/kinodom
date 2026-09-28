package api

import (
	"net/http"

	"kinodom/internal/httpx"
	"kinodom/internal/store"
	"kinodom/internal/supervisor"
)

// statusResponse растёт на следующих этапах: трекеры, прокси, квота Кинопоиска, место, сеть.
type statusResponse struct {
	Problems []store.Problem      `json:"problems"`
	Modules  []supervisor.Status `json:"modules"`
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	ps, err := s.deps.DB.Problems(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "не удалось прочитать проблемы: "+err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, statusResponse{Problems: ps, Modules: s.deps.Sup.Status()})
}
