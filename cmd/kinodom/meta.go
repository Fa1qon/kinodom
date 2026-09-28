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
	"strconv"
	"strings"

	"kinodom/internal/meta"
	"kinodom/internal/store"
)

const metaUsage = `Использование:
  kinodom meta title <название раздачи>
  kinodom meta kp quota | film <номер> | imdb <tt…> | search [--year N] <название> | rating <номер>
  kinodom meta rate [--kp N] [--imdb tt…] <название раздачи>
  kinodom meta image [--proxy URL] [--direct] <адрес картинки> | poster <номер Кинопоиска>
      флаги kp, rate, poster: --api URL (вместо kinopoiskapiunofficial.tech и rating.kinopoisk.ru)
      ключ Кинопоиска — переменная окружения KINODOM_KP_KEY
`

// cmdMeta — проверка метаданных вживую без сервера: разбор названий, Кинопоиск, картинки.
// «rate» проходит всю цепочку очереди рейтингов на временной базе.
func cmdMeta(args []string, stdout, stderr io.Writer) int {
	if len(args) < 2 {
		fmt.Fprint(stderr, metaUsage)
		return 2
	}
	fs := flag.NewFlagSet("meta", flag.ContinueOnError)
	fs.SetOutput(stderr)
	api := fs.String("api", "", "адрес API и рейтингов Кинопоиска вместо настоящих")
	year := fs.Int("year", 0, "год для поиска")
	kpID := fs.Int("kp", 0, "номер Кинопоиска из описания раздачи")
	imdb := fs.String("imdb", "", "номер IMDb из описания раздачи")
	proxy := fs.String("proxy", "", "прокси для картинок раздач: socks5://… или http://…")
	direct := fs.Bool("direct", false, "качать картинку напрямую, как постеры Кинопоиска")
	action, rest := args[0], args[1:]
	if action == "kp" {
		action, rest = "kp "+args[1], args[2:]
	}
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	kp := meta.NewKinopoisk(meta.KinopoiskOptions{Key: os.Getenv("KINODOM_KP_KEY"), APIBase: *api, RatingBase: *api})
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	arg := strings.Join(fs.Args(), " ")
	number := func() (int, bool) {
		n, err := strconv.Atoi(arg)
		return n, err == nil && n > 0
	}
	switch action {
	case "title":
		if arg == "" {
			break
		}
		t := meta.ParseTitle(arg)
		fmt.Fprintf(stdout, "Русское:      %s\nОригинальное: %s\nВсе названия: %s\nГод:          %d\nКачество:     %s\n",
			t.Ru, orDash(t.Orig), strings.Join(t.Names, " | "), t.Year, orDash(t.Quality))
		return 0
	case "kp quota":
		q, err := kp.Quota(ctx)
		if err != nil {
			return fail(stderr, err)
		}
		total := "нет"
		if q.TotalLimit >= 0 {
			total = fmt.Sprintf("%d из %d", q.TotalUsed, q.TotalLimit)
		}
		fmt.Fprintf(stdout, "За сутки: %d из %d\nОбщий лимит: %s\nАккаунт: %s\n", q.DailyUsed, q.DailyLimit, total, q.Account)
		return 0
	case "kp film", "kp rating":
		id, ok := number()
		if !ok {
			break
		}
		if action == "kp rating" {
			r, imdbR, err := kp.KeylessRating(ctx, id)
			if err != nil {
				return fail(stderr, err)
			}
			fmt.Fprintf(stdout, "Кинопоиск %.3f · IMDb %.1f (без ключа)\n", r, imdbR)
			return 0
		}
		f, err := kp.Film(ctx, id)
		if err != nil {
			return fail(stderr, err)
		}
		printFilm(stdout, f)
		return 0
	case "kp imdb":
		if arg == "" {
			break
		}
		f, err := kp.ByIMDb(ctx, arg)
		if err != nil {
			return fail(stderr, err)
		}
		printFilm(stdout, f)
		return 0
	case "kp search":
		if arg == "" {
			break
		}
		films, err := kp.Search(ctx, arg, *year)
		if err != nil {
			return fail(stderr, err)
		}
		fmt.Fprintf(stdout, "Найдено %d\n", len(films))
		for _, f := range films {
			printFilm(stdout, f)
		}
		return 0
	case "rate":
		if arg == "" && *kpID == 0 && *imdb == "" {
			break
		}
		return metaRate(ctx, kp, meta.Item{Release: "cli:1", KinopoiskID: *kpID, IMDbID: *imdb, Title: arg}, stdout, stderr)
	case "image", "poster":
		dir, err := os.MkdirTemp("", "kinodom-img-")
		if err != nil {
			return fail(stderr, err)
		}
		defer os.RemoveAll(dir)
		im, err := meta.NewImages(meta.ImagesOptions{Dir: dir, Proxy: *proxy})
		if err != nil {
			return fail(stderr, err)
		}
		src, via := arg, meta.ViaProxy
		if *direct {
			via = meta.Direct
		}
		if action == "poster" {
			id, ok := number()
			if !ok {
				break
			}
			src, via = kp.PosterURL(id), meta.Direct
		}
		if src == "" {
			break
		}
		key, err := im.Fetch(ctx, src, via)
		if errors.Is(err, meta.ErrNoImage) {
			fmt.Fprintln(stdout, "Картинки нет:", src)
			return 1
		}
		if err != nil {
			return fail(stderr, err)
		}
		files, _ := filepath.Glob(filepath.Join(dir, key+".*"))
		for _, f := range files {
			st, _ := os.Stat(f)
			fmt.Fprintf(stdout, "Картинка %s: %s, %d байт\n", key, filepath.Ext(f), st.Size())
		}
		return 0
	}
	fmt.Fprint(stderr, metaUsage)
	return 2
}

// metaRate — одна раздача через всю цепочку очереди рейтингов на временной базе.
func metaRate(ctx context.Context, kp *meta.Kinopoisk, it meta.Item, stdout, stderr io.Writer) int {
	dir, err := os.MkdirTemp("", "kinodom-rate-")
	if err != nil {
		return fail(stderr, err)
	}
	defer os.RemoveAll(dir)
	db, err := store.Open(ctx, filepath.Join(dir, "kinodom.db"))
	if err != nil {
		return fail(stderr, err)
	}
	defer db.Close()
	r := meta.NewRatings(meta.RatingsOptions{KP: kp, DB: db})
	if err := r.Enqueue(ctx, 1, it); err != nil {
		return fail(stderr, err)
	}
	t := meta.ParseTitle(it.Title)
	fmt.Fprintf(stdout, "Раздача: %s\nИщем: %q / %q, год %d\n", it.Title, t.Orig, t.Ru, t.Year)
	for i := 0; i < 5; i++ {
		did, err := r.Step(ctx)
		if err != nil {
			return fail(stderr, err)
		}
		if !did {
			break
		}
	}
	m, err := r.For(ctx, []string{it.Release})
	if err != nil {
		return fail(stderr, err)
	}
	st, _ := r.Status(ctx)
	got, ok := m[it.Release]
	switch {
	case ok:
		fmt.Fprintf(stdout, "Найдено: %s / %s (%d), Кинопоиск %d · рейтинг %.1f · IMDb %.1f\n",
			got.NameRu, got.NameOrig, got.Year, got.KinopoiskID, got.Kinopoisk, got.IMDb)
	case st.Queue > 0:
		fmt.Fprintln(stdout, "Не сейчас: задача отложена (сеть, квота или нет ключа)")
	default:
		fmt.Fprintln(stdout, "Не найдено")
	}
	return 0
}

func printFilm(w io.Writer, f meta.Film) {
	rating := "нет"
	if f.Rating > 0 {
		rating = fmt.Sprintf("%.1f", f.Rating)
	}
	fmt.Fprintf(w, "%d  %s / %s (%d) %s · рейтинг %s · IMDb %s\n", f.ID, orDash(f.NameRu), orDash(f.NameOrig), f.Year, f.Type, rating, orDash(f.IMDbID))
}
