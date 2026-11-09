package updates

import (
	"context"
	"errors"
	"net/http"
	"time"

	"kinodom/internal/httpx"
)

type Router interface {
	Handle(pattern, module string, h http.Handler)
	HandleHome(pattern, module string, h http.Handler)
}

// Register — состояние с любого устройства, проверка и переключатель — из домашней сети.
func (s *Service) Register(r Router) {
	r.Handle("GET /api/v1/upgrade", "", http.HandlerFunc(s.handleGet))
	r.HandleHome("POST /api/v1/upgrade/check", "", http.HandlerFunc(s.handleCheck))
	r.HandleHome("PUT /api/v1/upgrade", "", http.HandlerFunc(s.handlePut))
	r.HandleHome("POST /api/v1/upgrade/app", "", http.HandlerFunc(s.handleApp))
}

// handleApp — «Обновить»: установщик качается и запускается; сервер вскоре перезапустится новой версией.
func (s *Service) handleApp(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	if err := s.InstallApp(ctx); err != nil {
		code := 500
		if errors.Is(err, ErrNoApp) {
			code = 409
		}
		httpx.WriteError(w, code, err.Error())
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (s *Service) handleGet(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, 200, s.Status(r.Context()))
}

// handleCheck — «Проверить сейчас»: базы ставятся сразу, даже при выключенном автообновлении (человек сам
// попросил). Загрузка базы — минуты, поэтому идёт в фоне; ответ — состояние после проверки списка.
func (s *Service) handleCheck(w http.ResponseWriter, r *http.Request) {
	done := make(chan struct{})
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		s.Check(ctx, true)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
	case <-r.Context().Done():
	}
	httpx.WriteJSON(w, 200, s.Status(r.Context()))
}

func (s *Service) handlePut(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Auto *bool `json:"auto"`
	}
	if !httpx.ReadJSON(w, r, &in) {
		return
	}
	if in.Auto == nil {
		httpx.WriteError(w, 400, "ожидается auto")
		return
	}
	if err := s.SetAuto(r.Context(), *in.Auto); err != nil {
		httpx.WriteError(w, 500, err.Error())
		return
	}
	httpx.WriteJSON(w, 200, s.Status(r.Context()))
}
