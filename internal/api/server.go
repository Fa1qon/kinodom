// Package api — HTTP-сервер Kinodom: общий для всех модулей API, поток, пульт.
// Модули регистрируют свои маршруты через Handle / HandleLocal.
package api

import (
	"context"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"kinodom/internal/store"
	"kinodom/internal/supervisor"
)

type Deps struct {
	Log *slog.Logger
	DB  *store.DB
	Sup *supervisor.Supervisor
	Web fs.FS // файлы пульта (web.Static)
}

type Server struct {
	addr      string
	deps      Deps
	mux       *http.ServeMux
	hosts     *hostList
	ready     chan struct{}
	readyOnce sync.Once
	bound     atomic.Value // string: фактический адрес после Listen
}

func New(addr string, d Deps) *Server {
	s := &Server{
		addr:  addr,
		deps:  d,
		mux:   http.NewServeMux(),
		hosts: newHostList(),
		ready: make(chan struct{}),
	}
	s.mux.HandleFunc("GET /api/v1/status", s.handleStatus)
	s.mux.Handle("GET /", http.FileServerFS(d.Web))
	return s
}

func (s *Server) Name() string { return "api" }

// Handle регистрирует маршрут модуля. Пока модуль не работает, маршрут отвечает 503,
// а маршруты других модулей не затронуты. module == "" — без такой проверки.
func (s *Server) Handle(pattern, module string, h http.Handler) {
	s.mux.Handle(pattern, s.moduleGuard(module, h))
}

// HandleLocal — как Handle, но только для запросов с этого ПК (настройки, удаление, правки).
func (s *Server) HandleLocal(pattern, module string, h http.Handler) {
	s.mux.Handle(pattern, loopbackOnly(s.moduleGuard(module, h)))
}

// Handler — все проверки и маршруты. Порядок важен: сначала ловим панику,
// затем отсекаем чужой Host, затем проверяем Content-Type изменяющих запросов.
func (s *Server) Handler() http.Handler {
	return recoverer(s.deps.Log, s.hosts.guard(jsonGuard(s.mux)))
}

// Run слушает адрес и работает до отмены ctx.
func (s *Server) Run(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return err
	}
	s.bound.Store(ln.Addr().String())
	s.readyOnce.Do(func() { close(s.ready) })
	// Без WriteTimeout: поток фильма идёт часами (спека, раздел 9).
	srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case <-ctx.Done():
		shCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shCtx)
		return nil
	case err := <-errc:
		return err
	}
}

// Ready закрывается, когда сервер впервые начал слушать адрес.
func (s *Server) Ready() <-chan struct{} { return s.ready }

// Addr — фактический адрес (важно, когда слушаем порт 0 в тестах).
func (s *Server) Addr() string {
	v, _ := s.bound.Load().(string)
	return v
}
