package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"

	"kinodom/internal/app"
)

// cmdRun — сервер в консоли для разработки; Ctrl+C останавливает.
func cmdRun(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		fmt.Fprintln(stderr, "команда run не принимает аргументов")
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	// app.New сразу занимает порт API: если он занят, запуск отказывает с понятной причиной.
	a, err := app.New(ctx, app.Options{Console: true})
	if err != nil {
		fmt.Fprintln(stderr, "не удалось запустить:", err)
		return 1
	}
	defer a.Close()
	fmt.Fprintf(stdout, "Kinodom работает: http://localhost:%d  (остановить — Ctrl+C)\n", a.Boot.APIPort)
	a.Run(ctx)
	return 0
}
