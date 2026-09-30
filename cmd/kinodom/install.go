package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"

	"kinodom/internal/config"
	"kinodom/internal/setup"
	"kinodom/internal/winsvc"
)

// newSystem — системные операции этого ПК (тесты подменяют подделкой).
var newSystem = winsvc.Real

// programDir — папка kinodom.exe: рядом лежит kinodomw.exe.
func programDir() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.Dir(exe), nil
}

// cmdInstall — установка или починка на этом ПК (спека этапа 11a, раздел 4.2). Вызывает установщик.
func cmdInstall(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	fs.SetOutput(stderr)
	downloads := fs.String("downloads", "", "папка загрузок: заменяет сохранённую; пусто — не менять")
	downloadsDefault := fs.String("downloads-default", "", "папка загрузок, только если её ещё нет в настройках (установщик)")
	noStart := fs.Bool("no-start", false, "не запускать службу")
	result := fs.String("result", "", "файл для текста отказа (UTF-8; его читает установщик)")
	check := fs.Bool("check", false, "только проверить, пройдёт ли установка (до копирования файлов)")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		fmt.Fprintln(stderr, "использование: kinodom install [--check] [--downloads ПАПКА | --downloads-default ПАПКА] [--no-start] [--result ФАЙЛ]")
		return 2
	}
	if *check {
		return finish(stderr, *result, installCheck(*downloads, *downloadsDefault))
	}
	return finish(stderr, *result, install(*downloads, *downloadsDefault, *noStart, stdout))
}

// installCheck — установщик до копирования файлов: права, папка загрузок, порт. kinodom.json не
// создаётся: проверка не должна ничего оставлять.
func installCheck(downloads, downloadsDefault string) error {
	paths := config.NewPaths(config.DefaultHome())
	boot := config.DefaultBootstrap()
	if _, err := os.Stat(paths.Bootstrap); err == nil {
		b, err := config.LoadBootstrap(paths.Bootstrap)
		if err != nil {
			return err
		}
		boot = b
	}
	return setup.Check(context.Background(), newSystem(), setup.InstallOptions{Downloads: downloads, DownloadsDefault: downloadsDefault,
		Home: paths.Home, APIPort: boot.APIPort, TorrentPort: boot.TorrentPort})
}

// finish — код выхода системной команды; отказ — ещё и текстом в файл result (UTF-8): его читает
// установщик, вывод консоли ему в понятной кодировке не достать.
func finish(stderr io.Writer, result string, err error) int {
	if result != "" {
		text := ""
		if err != nil {
			text = err.Error()
		}
		os.WriteFile(result, []byte(text), 0o644)
	}
	if err != nil {
		return fail(stderr, err)
	}
	return 0
}

func install(downloads, downloadsDefault string, noStart bool, stdout io.Writer) error {
	sys := newSystem()
	if !sys.IsAdmin() {
		return setup.ErrNotAdmin
	}
	dir, err := programDir()
	if err != nil {
		return err
	}
	paths := config.NewPaths(config.DefaultHome())
	if err := os.MkdirAll(paths.Home, 0o755); err != nil {
		return err
	}
	boot, err := config.LoadBootstrap(paths.Bootstrap) // нет файла — создаётся с портами по умолчанию
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	err = setup.Install(ctx, sys, setup.InstallOptions{Downloads: downloads, DownloadsDefault: downloadsDefault, NoStart: noStart, Home: paths.Home,
		ProgramDir: dir, APIPort: boot.APIPort, TorrentPort: boot.TorrentPort}, func(s string) { fmt.Fprintln(stdout, s) })
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Kinodom установлен: http://localhost:%d\n", boot.APIPort)
	return nil
}

// cmdUninstall — удаление службы, правил и ссылки; --purge — ещё настройки и скачанное.
func cmdUninstall(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	fs.SetOutput(stderr)
	purge := fs.Bool("purge", false, "удалить также настройки и скачанное")
	result := fs.String("result", "", "файл для текста отказа (UTF-8; его читает программа удаления)")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		fmt.Fprintln(stderr, "использование: kinodom uninstall [--purge] [--result ФАЙЛ]")
		return 2
	}
	dir, err := programDir()
	if err != nil {
		return fail(stderr, err)
	}
	err = setup.Uninstall(context.Background(), newSystem(), setup.UninstallOptions{Purge: *purge, Home: config.DefaultHome(), ProgramDir: dir},
		func(s string) { fmt.Fprintln(stdout, s) })
	if err == nil {
		fmt.Fprintln(stdout, "Kinodom удалён")
	}
	return finish(stderr, *result, err)
}

// cmdStop — остановить службу и дождаться остановки: установщик вызывает её перед заменой файлов.
// Перезапуск при сбое снимается (иначе упавшая служба поднялась бы посреди копирования) и
// возвращается следующим kinodom install. Службы нет — ничего не делает.
func cmdStop(args []string, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		fmt.Fprintln(stderr, "использование: kinodom stop")
		return 2
	}
	sys := newSystem()
	if dir, err := programDir(); err == nil {
		// Значки в трее держат kinodomw.exe — установщик его заменяет. После установки значок
		// запускается снова.
		if err := sys.Procs.Close(filepath.Join(dir, "kinodomw.exe")); err != nil {
			fmt.Fprintln(stderr, "Значок в трее не закрылся:", err)
		}
	}
	err := sys.SCM.Stop(setup.ServiceName, setup.StopWait)
	switch {
	case errors.Is(err, winsvc.ErrNotInstalled):
		return 0
	case err != nil:
		return fail(stderr, fmt.Errorf("служба Kinodom не остановилась: %w", err))
	}
	fmt.Fprintln(stdout, "Служба Kinodom остановлена")
	return 0
}
