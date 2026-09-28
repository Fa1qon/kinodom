package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"time"

	"kinodom/internal/source"
	"kinodom/internal/source/rutor"
)

const sourceUsage = `Использование:
  kinodom source rutor top [флаги] <категория>          топ категории по раздающим (12 — научпоп)
  kinodom source rutor search [флаги] <запрос>          поиск по видеокатегориям
  kinodom source rutor details [флаги] <номер>          страница раздачи
  kinodom source rutor torrent [флаги] <номер> <файл>   скачать .torrent
Флаги: --proxy socks5://… | --limit N | --mirror URL (можно несколько) | --download URL
`

// cmdSource — проверка источника раздач вживую, без каталога (каталог — этап 5).
// Работает без сервера: сама ходит на трекер.
func cmdSource(args []string, stdout, stderr io.Writer) int {
	if len(args) < 2 || args[0] != "rutor" {
		fmt.Fprint(stderr, sourceUsage)
		return 2
	}
	action := args[1]
	fs := flag.NewFlagSet("source", flag.ContinueOnError)
	fs.SetOutput(stderr)
	proxy := fs.String("proxy", "", "прокси для трекера: socks5://… или http://…; пусто — напрямую")
	limit := fs.Int("limit", 20, "сколько строк показать")
	var mirrors listFlag
	fs.Var(&mirrors, "mirror", "зеркало вместо встроенных; можно несколько раз")
	download := fs.String("download", "", "адрес для .torrent вместо "+rutor.DefaultDownloadBase)
	if err := fs.Parse(args[2:]); err != nil {
		return 2
	}
	want := map[string]int{"top": 1, "search": -1, "details": 1, "torrent": 2}[action]
	if want == 0 || (want > 0 && fs.NArg() != want) || (want < 0 && fs.NArg() == 0) {
		fmt.Fprint(stderr, sourceUsage)
		return 2
	}
	src, err := rutor.New(rutor.Options{
		Proxy: *proxy, Mirrors: mirrors, DownloadBase: *download,
		// Предупреждения (зеркало не ответило, повтор запроса) — в поток ошибок.
		Log: slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelWarn})),
	})
	if err != nil {
		return fail(stderr, err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	start := time.Now()
	switch action {
	case "top":
		rs, err := src.Top(ctx, fs.Arg(0), 0)
		if err != nil {
			return fail(stderr, err)
		}
		printReleases(stdout, rs, *limit)
	case "search":
		rs, err := src.Search(ctx, strings.Join(fs.Args(), " "))
		var partial *source.PartialError
		if err != nil && !errors.As(err, &partial) {
			return fail(stderr, err)
		}
		printReleases(stdout, rs, *limit)
		if partial != nil {
			fmt.Fprintln(stderr, "Внимание:", partial)
		}
	case "details":
		d, err := src.Details(ctx, fs.Arg(0))
		if err != nil {
			return fail(stderr, err)
		}
		printDetails(stdout, d)
	case "torrent":
		b, err := src.Torrent(ctx, fs.Arg(0))
		if err != nil {
			return fail(stderr, err)
		}
		if err := os.WriteFile(fs.Arg(1), b, 0o644); err != nil {
			return fail(stderr, err)
		}
		fmt.Fprintf(stdout, "Сохранено: %s (%d байт)\n", fs.Arg(1), len(b))
		fmt.Fprintf(stdout, "\nГотово за %.1f с\n", time.Since(start).Seconds())
		return 0
	}
	fmt.Fprintf(stdout, "\nГотово за %.1f с, ответило зеркало %s\n", time.Since(start).Seconds(), src.Mirror())
	return 0
}

func fail(w io.Writer, err error) int {
	fmt.Fprintln(w, "Ошибка:", err)
	return 1
}

// listFlag — флаг, который можно повторить: --mirror a --mirror b.
type listFlag []string

func (l *listFlag) String() string     { return strings.Join(*l, ",") }
func (l *listFlag) Set(v string) error { *l = append(*l, v); return nil }

func printReleases(w io.Writer, rs []source.Release, limit int) {
	fmt.Fprintf(w, "Найдено %d раздач\n\n", len(rs))
	fmt.Fprintf(w, "%6s %6s %9s  %-9s %s\n", "Разд.", "Кач.", "Размер", "Добавлена", "Название [номер]")
	for i, r := range rs {
		if i == limit {
			break
		}
		fmt.Fprintf(w, "%6d %6d %9s  %-9s %s [%s]\n", r.Seeders, r.Leechers, humanSize(r.Size), r.Added.Format("02.01.06"), r.Title, r.TopicID)
	}
}

func printDetails(w io.Writer, d source.Details) {
	fmt.Fprintln(w, d.Title)
	fmt.Fprintf(w, "Категория %s · раздают %d · качают %d · %s · добавлена %s\n",
		d.CategoryID, d.Seeders, d.Leechers, humanSize(d.Size), d.Added.Format("02.01.2006 15:04"))
	fmt.Fprintln(w, "Постер:   ", orDash(d.PosterURL))
	fmt.Fprintln(w, "Кинопоиск:", orDash(d.KinopoiskID))
	fmt.Fprintln(w, "IMDb:     ", orDash(d.IMDbID))
	fmt.Fprintln(w, ".torrent: ", d.TorrentURL)
	fmt.Fprintln(w, "magnet:   ", d.Magnet)
	fmt.Fprintf(w, "\n%s\n", excerpt(d.Description, 600))
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func excerpt(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// humanSize — размер для людей: «3.9 ГБ», «812 МБ».
func humanSize(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f ГБ", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.0f МБ", float64(n)/(1<<20))
	case n > 0:
		return fmt.Sprintf("%d КБ", n>>10)
	}
	return "?"
}
