// Package api — HTTP-сервер Kinodom: общий для всех модулей API, поток, пульт.
// Модули регистрируют свои маршруты через Handle / HandleHome / HandleLocal.
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

	lnMu sync.Mutex
	ln   net.Listener // занятый заранее порт; Run забирает его

	statusMu sync.Mutex
	status   StatusFunc  // поля «Состояния» от приложения; nil — только проблемы и модули
	protocol func() bool // обработчик kinodom:// зарегистрирован; nil — поле protocol не отдаётся
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
	s.mux.Handle("GET /", pultHandler(d.Web))
	return s
}

func (s *Server) Name() string { return "api" }

// Handle регистрирует маршрут модуля. Пока модуль не работает, маршрут отвечает 503,
// а маршруты других модулей не затронуты. module == "" — без такой проверки.
func (s *Server) Handle(pattern, module string, h http.Handler) {
	s.mux.Handle(pattern, s.moduleGuard(module, h))
}

// HandleHome — как Handle, но только для запросов из домашней сети: изменение настроек, удаление,
// правки (спека этапа 7, раздел 10.1).
func (s *Server) HandleHome(pattern, module string, h http.Handler) {
	s.mux.Handle(pattern, homeOnly(s.moduleGuard(module, h)))
}

// HandleLocal — как Handle, но только для запросов с этого ПК (открыть раздачу по произвольной ссылке).
func (s *Server) HandleLocal(pattern, module string, h http.Handler) {
	s.mux.Handle(pattern, thisPCOnly(s.moduleGuard(module, h)))
}

// Handler — все проверки и маршруты. Порядок важен: сначала ловим панику,
// затем отсекаем чужой Host, затем проверяем Content-Type изменяющих запросов.
func (s *Server) Handler() http.Handler {
	return recoverer(s.deps.Log, s.hosts.guard(jsonGuard(s.mux)))
}

// Listen занимает порт API заранее — чтобы запуск сразу узнал, что порт занят,
// а не писал «работает». Порт берётся эксклюзивно (см. listenExclusive).
func (s *Server) Listen() error {
	s.lnMu.Lock()
	defer s.lnMu.Unlock()
	if s.ln != nil {
		return nil
	}
	ln, err := listenExclusive(s.addr)
	if err != nil {
		return err
	}
	s.ln = ln
	s.bound.Store(ln.Addr().String())
	return nil
}

// takeListener отдаёт занятый порт (или занимает его); следующий Run после остановки
// сервера откроет порт заново.
func (s *Server) takeListener() (net.Listener, error) {
	if err := s.Listen(); err != nil {
		return nil, err
	}
	s.lnMu.Lock()
	defer s.lnMu.Unlock()
	ln := s.ln
	s.ln = nil
	return ln, nil
}

// Run обслуживает порт до отмены ctx.
func (s *Server) Run(ctx context.Context) error {
	ln, err := s.takeListener()
	if err != nil {
		return err
	}
	s.readyOnce.Do(func() { close(s.ready) })
	supervisor.Ready(ctx)
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
