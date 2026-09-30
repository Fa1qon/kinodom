package api

import (
	"context"
	"net/http"

	"kinodom/internal/httpx"
)

// StatusFunc — поля «Состояния», которые знает приложение, а не сервер API: трекеры, квота
// Кинопоиска, место, потоки (спека этапа 7, раздел 5.7).
type StatusFunc func(ctx context.Context) (map[string]any, error)

// SetProtocolCheck подключает проверку «обработчик kinodom:// зарегистрирован» (хвост Х33): запросу с
// этого ПК «Состояние» отдаёт её в поле protocol.
func (s *Server) SetProtocolCheck(f func() bool) {
	s.statusMu.Lock()
	s.protocol = f
	s.statusMu.Unlock()
}

// SetStatus подключает поля приложения к GET /api/v1/status.
func (s *Server) SetStatus(f StatusFunc) {
	s.statusMu.Lock()
	s.status = f
	s.statusMu.Unlock()
}

// handleStatus — «Состояние»: local (запрос с этого ПК — пульт по нему решает, открывать ли плеер по
// kinodom://), canEdit (из домашней сети — предлагать ли изменение настроек и удаление), проблемы,
// модули и поля приложения.
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	ps, err := s.deps.DB.Problems(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "не удалось прочитать проблемы: "+err.Error())
		return
	}
	local := httpx.FromThisPC(r)
	out := map[string]any{"local": local, "canEdit": httpx.FromHome(r), "problems": ps, "modules": s.deps.Sup.Status()}
	s.statusMu.Lock()
	extra, protocol := s.status, s.protocol
	s.statusMu.Unlock()
	if local && protocol != nil {
		out["protocol"] = protocol()
	}
	if extra != nil {
		fields, err := extra(r.Context())
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "не удалось собрать состояние: "+err.Error())
			return
		}
		for k, v := range fields {
			out[k] = v
		}
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}
