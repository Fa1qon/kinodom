package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"

	"kinodom/internal/app"
)

// cmdRun — сервер в консоли для разработки; Ctrl+C останавливает.
func cmdRun(args []string, stdout, stderr io.Writer) int {
	o, err := runOptions(args)
	if err != nil {
		fmt.Fprintln(stderr, err)
		fmt.Fprintln(stderr, "использование: kinodom run [--downloads ПАПКА]  (папка данных — KINODOM_HOME)")
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	// app.New сразу занимает порт API: если он занят, запуск отказывает с понятной причиной.
	a, err := app.New(ctx, o)
	if err != nil {
		fmt.Fprintln(stderr, "не удалось запустить:", err)
		return 1
	}
	defer a.Close()
	fmt.Fprintf(stdout, "Kinodom работает: http://localhost:%d  (остановить — Ctrl+C)\n", a.Boot.APIPort)
	a.Run(ctx)
	return 0
}

// runOptions — флаги run. --downloads — папка загрузок вместо настройки downloads.dir: проверка
// вживую на отдельной папке, не трогая загрузки службы.
func runOptions(args []string) (app.Options, error) {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	downloads := fs.String("downloads", "", "папка загрузок")
	if err := fs.Parse(args); err != nil {
		return app.Options{}, err
	}
	if fs.NArg() > 0 {
		return app.Options{}, fmt.Errorf("лишние аргументы: %v", fs.Args())
	}
	return app.Options{Console: true, DownloadsDir: *downloads}, nil
}
