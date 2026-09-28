package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"

	"kinodom/internal/app"
	"kinodom/internal/config"
)

// cmdRun — сервер в консоли для разработки; Ctrl+C останавливает.
func cmdRun(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		fmt.Fprintln(stderr, "команда run не принимает аргументов")
		return 2
	}
	home := config.DefaultHome()
	boot, err := config.LoadBootstrap(config.NewPaths(home).Bootstrap)
	if err != nil {
		fmt.Fprintln(stderr, "ошибка конфигурации:", err)
		return 1
	}
	if err := checkPortFree(boot.APIPort); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	a, err := app.New(ctx, app.Options{Home: home, Console: true})
	if err != nil {
		fmt.Fprintln(stderr, "не удалось запустить:", err)
		return 1
	}
	defer a.Close()
	fmt.Fprintf(stdout, "Kinodom работает: http://localhost:%d  (остановить — Ctrl+C)\n", boot.APIPort)
	a.Run(ctx)
	return 0
}

// checkPortFree отвечает понятной ошибкой, если порт API занят — обычно это работающая служба Kinodom.
func checkPortFree(port int) error {
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return fmt.Errorf("порт %d занят: Kinodom уже запущен (служба или другая копия) либо порт занят другой программой", port)
	}
	return ln.Close()
}
