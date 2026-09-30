package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"kinodom/internal/app"
	"kinodom/internal/catalog"
)

const catalogUsage = `Использование:
  kinodom catalog refresh --home ПАПКА [--wait 3m] [--limit 20] — обновить каталог и дождаться
      догрузки первых карточек
  kinodom catalog list --home ПАПКА [--tracker T] [--category N] [--limit 20]
  kinodom catalog search --home ПАПКА <запрос>
      флаги: --proxy URL | --rutor URL | --rutor-download URL | --rutracker URL | --rutracker-api URL |
             --rutracker-feed URL | --kinopoisk URL | --no-edge | --sections «rutor:12,rutracker:46+»
      логин и пароль Rutracker — KINODOM_RUTRACKER_LOGIN и KINODOM_RUTRACKER_PASSWORD, ключ
      Кинопоиска — KINODOM_KP_KEY (в базу не записываются); разделы — настройка catalog.categories
`

// cmdCatalog — каталог вживую без службы: те же модули, что у сервера, на отдельной папке
// (--home обязателен — чтобы не трогать данные работающей службы).
func cmdCatalog(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, catalogUsage)
		return 2
	}
	fs := flag.NewFlagSet("catalog", flag.ContinueOnError)
	fs.SetOutput(stderr)
	home := fs.String("home", "", "папка данных Kinodom для проверки")
	wait := fs.Duration("wait", 3*time.Minute, "сколько ждать догрузки")
	limit := fs.Int("limit", 20, "сколько карточек показать")
	tracker := fs.String("tracker", "", "только этот трекер")
	category := fs.String("category", "", "только этот раздел")
	proxy := fs.String("proxy", "", "прокси для трекеров")
	sections := fs.String("sections", "", "разделы каталога вместо настройки catalog.categories")
	var t app.Trackers
	rutorURL := fs.String("rutor", "", "адрес Rutor вместо настройки rutor.address")
	rtURL := fs.String("rutracker", "", "адрес Rutracker вместо настройки rutracker.address")
	fs.StringVar(&t.RutorDownload, "rutor-download", "", "адрес .torrent Rutor; пусто — по правилу из адреса сайта")
	fs.StringVar(&t.RutrackerAPI, "rutracker-api", "", "API Rutracker; пусто — по правилу из адреса сайта")
	kinopoisk := fs.String("kinopoisk", "", "API и рейтинги Кинопоиска вместо настоящих")
	fs.StringVar(&t.RutrackerFeed, "rutracker-feed", "", "лента Rutracker; пусто — по правилу из адреса сайта")
	fs.BoolVar(&t.NoEdge, "no-edge", false, "без Edge: пропуск Cloudflare не добывать")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	action := args[0]
	if *home == "" || (action == "search") != (fs.NArg() > 0) || (action != "refresh" && action != "list" && action != "search") {
		fmt.Fprint(stderr, catalogUsage)
		return 2
	}
	if *rutorURL != "" {
		t.RutorMirrors = []string{*rutorURL}
	}
	if *rtURL != "" {
		t.RutrackerMirrors = []string{*rtURL}
	}
	settings := map[string]string{
		"rutracker.login":    os.Getenv("KINODOM_RUTRACKER_LOGIN"),
		"rutracker.password": os.Getenv("KINODOM_RUTRACKER_PASSWORD"),
		"kinopoisk.key":      os.Getenv("KINODOM_KP_KEY"),
		"proxy.trackers":     *proxy,
	}
	if *sections != "" {
		settings["catalog.categories"] = *sections
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	a, err := app.New(ctx, app.Options{Home: *home, ListenAddr: "127.0.0.1:0", Offline: true,
		DownloadsDir: filepath.Join(*home, "downloads"), Settings: settings, Trackers: t, KinopoiskAPI: *kinopoisk})
	if err != nil {
		return fail(stderr, err)
	}
	defer a.Close()
	if action == "list" {
		return printCatalog(ctx, a, catalog.ListOptions{Tracker: *tracker, Category: *category, Limit: *limit}, stdout, stderr)
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { a.Run(runCtx); close(done) }()
	defer func() { cancel(); <-done }()
	if action == "search" {
		return runCatalogSearch(ctx, a, strings.Join(fs.Args(), " "), stdout, stderr)
	}
	start := time.Now()
	deadline := start.Add(*wait)
	for time.Now().Before(deadline) && ctx.Err() == nil {
		es, total, err := a.Catalog.List(ctx, catalog.ListOptions{Limit: *limit})
		if err != nil {
			return fail(stderr, err)
		}
		ready := 0
		for _, e := range es {
			if e.Title != "" {
				ready++
			}
		}
		fmt.Fprintf(stderr, "%3.0f с: карточек %d, из первых %d с названием %d\n", time.Since(start).Seconds(), total, len(es), ready)
		if total > 0 && ready == len(es) {
			break
		}
		time.Sleep(2 * time.Second)
	}
	return printCatalog(ctx, a, catalog.ListOptions{Limit: *limit}, stdout, stderr)
}

func printCatalog(ctx context.Context, a *app.App, o catalog.ListOptions, stdout, stderr io.Writer) int {
	es, total, err := a.Catalog.List(ctx, o)
	if err != nil {
		return fail(stderr, err)
	}
	fmt.Fprintf(stdout, "Карточек %d\n\n", total)
	printEntries(stdout, es)
	cats, err := a.Catalog.Categories(ctx)
	if err != nil {
		return fail(stderr, err)
	}
	fmt.Fprintln(stdout, "\nРазделы:")
	for _, c := range cats {
		fmt.Fprintf(stdout, "  %s:%s  %s — %d\n", c.Tracker, c.ID, c.Name, c.Count)
	}
	ps, _ := a.DB.Problems(ctx)
	if len(ps) > 0 {
		fmt.Fprintln(stdout, "\nПроблемы:")
		for _, p := range ps {
			fmt.Fprintln(stdout, "  "+p.Text)
		}
	}
	return 0
}

func printEntries(w io.Writer, es []catalog.Entry) {
	fmt.Fprintf(w, "%6s %9s  %-4s %-3s %s\n", "Разд.", "Размер", "КП", "Кар", "Название [трекер:номер]")
	for _, e := range es {
		title, rating, img := e.Title, "—", "—"
		if title == "" {
			title = "(название загружается)"
		}
		if e.Rating.Kinopoisk > 0 {
			rating = fmt.Sprintf("%.1f", e.Rating.Kinopoisk)
		}
		if e.ImageKey != "" {
			img = "да"
		}
		fmt.Fprintf(w, "%6d %9s  %-4s %-3s %s [%s:%s]\n", e.Seeders, humanSize(e.Size), rating, img, title, e.Tracker, e.TopicID)
	}
}

func runCatalogSearch(ctx context.Context, a *app.App, q string, stdout, stderr io.Writer) int {
	start := time.Now()
	for {
		st, err := a.Catalog.Search(ctx, q)
		if err != nil {
			return fail(stderr, err)
		}
		fmt.Fprintf(stderr, "%4.1f с: найдено %d, трекеры %v\n", time.Since(start).Seconds(), len(st.Results), st.Trackers)
		if st.Complete {
			printEntries(stdout, st.Results)
			return 0
		}
		select {
		case <-ctx.Done():
			return 1
		case <-time.After(time.Second):
		}
	}
}
