//go:build windows

package main

import (
	"context"
	"fmt"
	"io"

	"golang.org/x/sys/windows/svc"

	"kinodom/internal/app"
	"kinodom/internal/setup"
)

// cmdService — сервер под управлением диспетчера служб Windows (спека этапа 11a, раздел 4.1): тот
// же сервер, что kinodom run; «остановить» и «завершение работы» — штатное закрытие.
func cmdService(args []string, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		fmt.Fprintln(stderr, "использование: kinodom service  (запускает диспетчер служб Windows)")
		return 2
	}
	if in, err := svc.IsWindowsService(); err != nil || !in {
		fmt.Fprintln(stderr, "kinodom service запускает диспетчер служб Windows; сервер в консоли — kinodom run")
		return 2
	}
	h := &serviceHandler{start: func(ctx context.Context) (server, error) {
		return app.New(ctx, app.Options{Version: version})
	}}
	if err := svc.Run(setup.ServiceName, h); err != nil {
		return 1
	}
	return 0
}

// server — то, чем управляет служба: приложение Kinodom.
type server interface {
	Run(ctx context.Context)
	Close() error
}

// serviceHandler — обработчик команд диспетчера служб.
type serviceHandler struct {
	start func(ctx context.Context) (server, error)
}

// Коды выхода службы: не ноль — Windows перезапускает её через 5 с (восстановление и при выходе
// с ошибкой без падения).
const (
	exitStartFailed = 1 // приложение не запустилось (причина — в журнале)
	exitStopped     = 2 // приложение остановилось само
)

func (h *serviceHandler) Execute(_ []string, r <-chan svc.ChangeRequest, s chan<- svc.Status) (bool, uint32) {
	s <- svc.Status{State: svc.StartPending}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a, err := h.start(ctx)
	if err != nil {
		return true, exitStartFailed
	}
	done := make(chan struct{})
	go func() { a.Run(ctx); close(done) }()
	s <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for {
		select {
		case c := <-r:
			switch c.Cmd {
			case svc.Interrogate:
				s <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				s <- svc.Status{State: svc.StopPending, WaitHint: 60000} // торренты закрываются не сразу
				cancel()
				<-done
				a.Close()
				return false, 0
			}
		case <-done:
			a.Close()
			return true, exitStopped
		}
	}
}
