// Package app собирает сервер из модулей. Его используют команда run, служба (этап 11)
// и интеграционные тесты — поэтому «все модули вместе» везде собираются одинаково.
package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"

	"golang.org/x/time/rate"

	"kinodom/internal/api"
	"kinodom/internal/catalog"
	"kinodom/internal/config"
	"kinodom/internal/edge"
	"kinodom/internal/logx"
	"kinodom/internal/meta"
	"kinodom/internal/netx"
	"kinodom/internal/source"
	"kinodom/internal/source/rutor"
	"kinodom/internal/source/rutracker"
	"kinodom/internal/store"
	"kinodom/internal/supervisor"
	"kinodom/internal/torrents"
	"kinodom/web"
)

// DefaultDownloadsDir — папка загрузок, пока её не выбрали в инсталляторе или настройках.
const DefaultDownloadsDir = `C:\Kinodom`

type Options struct {
	Home         string // корневая папка; пусто — config.DefaultHome()
	Console      bool   // дублировать журнал в консоль
	ListenAddr   string // адрес API; пусто — ":<apiPort>" из kinodom.json
	Offline      bool   // торрент-движок без сети, на случайном порту (тесты)
	DownloadsDir string // папка загрузок; пусто — настройка downloads.dir
	KinopoiskAPI string // адрес API и рейтингов Кинопоиска вместо настоящих (тесты)
	// Settings — поверх настроек из базы и не сохраняются: тесты и kinodom catalog (логин, пароль
	// Rutracker и ключ Кинопоиска — из переменных окружения, не в базу).
	Settings map[string]string
	Trackers Trackers // адреса трекеров вместо настоящих (тесты, kinodom catalog)
}

// Trackers — адреса трекеров вместо встроенных. Пусто — встроенные.
type Trackers struct {
	RutorMirrors     []string
	RutorDownload    string
	RutrackerMirrors []string
	RutrackerAPI     string
	RutrackerFeed    string
	NoEdge           bool       // без Edge: пропуск Cloudflare не добывается, форум Rutracker — только если открыт
	Rate             rate.Limit // запросов в секунду на трекер; 0 — 1 (тесты ускоряют)
}

type App struct {
	Paths    config.Paths
	Boot     config.Bootstrap
	Log      *slog.Logger
	DB       *store.DB
	Sup      *supervisor.Supervisor
	API      *api.Server
	Torrents *torrents.Service // nil, если движок не запустился (см. проблему torrents.engine)
	Ratings  *meta.Ratings     // рейтинги Кинопоиска (модуль ratings)
	Images   *meta.Images      // картинки, которые сервер отдаёт по /img/{key}
	Catalog  *catalog.Catalog  // каталог и поиск (модуль catalog)

	opts    Options
	kp      *meta.Kinopoisk
	closers []io.Closer // закрываются в обратном порядке
}

func New(ctx context.Context, o Options) (*App, error) {
	home := o.Home
	if home == "" {
		home = config.DefaultHome()
	}
	a := &App{Paths: config.NewPaths(home), opts: o}
	if err := a.Paths.Ensure(); err != nil {
		return nil, fmt.Errorf("папки Kinodom: %w", err)
	}
	// Журнал — первым: служба работает без консоли, и причина любого отказа запуска
	// должна остаться в kinodom.log (спека, раздел 4).
	log, logCloser, err := logx.New(a.Paths.Logs, o.Console)
	if err != nil {
		return nil, fmt.Errorf("журнал: %w", err)
	}
	a.Log = log
	a.closers = append(a.closers, logCloser)
	fail := func(err error) (*App, error) {
		log.Error("Kinodom не запустился", "err", err)
		a.Close()
		return nil, err
	}

	boot, err := config.LoadBootstrap(a.Paths.Bootstrap)
	if err != nil {
		return fail(err)
	}
	a.Boot = boot
	db, err := store.Open(ctx, a.Paths.DB)
	if errors.Is(err, store.ErrSchemaNewer) {
		return fail(fmt.Errorf("база %s: %w — установите версию Kinodom, которая её обновила, или восстановите копию базы (kinodom.db.bak-v*)", a.Paths.DB, err))
	}
	if err != nil {
		return fail(fmt.Errorf("база: %w", err))
	}
	a.DB = db
	a.closers = append(a.closers, db)

	a.Sup = supervisor.New(log, supervisor.WithErrorSink(func(module, text string) {
		if err := db.AddError(context.Background(), module, text); err != nil {
			log.Error("не удалось записать ошибку модуля", "err", err)
		}
	}))
	addr := o.ListenAddr
	if addr == "" {
		addr = fmt.Sprintf(":%d", boot.APIPort)
	}
	a.API = api.New(addr, api.Deps{Log: log, DB: db, Sup: a.Sup, Web: web.Static})
	// Порт занимаем сразу: занятый порт — отказ запуска, а не сервер «наполовину».
	if err := a.API.Listen(); err != nil {
		return fail(err)
	}
	a.Sup.Add(a.API, true) // API выключать нельзя: без него нет ни пульта, ни телевизоров

	a.initTorrents(ctx, o)
	if err := a.initMeta(ctx, o); err != nil {
		return fail(err)
	}
	if err := a.initCatalog(ctx, o); err != nil {
		return fail(err)
	}
	// Следующие этапы добавляют сюда свои модули так же: a.Sup.Add(m, a.ModuleEnabled(ctx, m.Name())).
	return a, nil
}

// initTorrents добавляет модуль торрентов. Движок создаётся внутри модуля: если папка
// загрузок недоступна, сервер работает без торрентов (проблема в «Состоянии», маршруты — 503),
// а сторож повторяет попытки, пока папка не появится.
func (a *App) initTorrents(ctx context.Context, o Options) {
	downloads := o.DownloadsDir
	if downloads == "" {
		downloads = a.setting(ctx, "downloads.dir", DefaultDownloadsDir)
	}
	proxy := a.setting(ctx, "proxy.trackers", "")
	if _, err := netx.ParseProxy(proxy); err != nil {
		a.setProblem(ctx, "proxy.invalid", "Прокси в настройках не работает: "+err.Error())
		proxy = "" // без прокси торренты работают, только анонсы Rutracker могут не пройти
	} else {
		a.clearProblem(ctx, "proxy.invalid")
	}
	upMBps, err := strconv.ParseFloat(a.setting(ctx, "torrents.uploadLimitMBps", "2"), 64)
	if err != nil || upMBps < 0 {
		upMBps = 2
	}
	port := a.Boot.TorrentPort
	if o.Offline {
		port = 0
	}
	log := a.Log.With("module", "torrents")
	cfg := torrents.Config{
		DownloadsDir: downloads,
		StateDir:     a.Paths.Torrent,
		ListenPort:   port,
		UploadLimit:  upMBps * 1024 * 1024,
		TrackerProxy: proxy,
		Offline:      o.Offline,
		Log:          log,
	}
	a.Torrents = torrents.NewLazyService(
		func() (*torrents.Engine, error) { return torrents.NewEngine(cfg) },
		torrents.NewRegistry(a.DB), log,
		func(err error) {
			if err != nil {
				log.Error("торрент-движок не запустился", "err", err)
				a.setProblem(context.Background(), "torrents.engine", "Торренты не работают: "+err.Error())
				return
			}
			a.clearProblem(context.Background(), "torrents.engine")
		})
	a.closers = append(a.closers, closerFunc(func() error {
		if e := a.Torrents.Engine(); e != nil {
			return e.Close()
		}
		return nil
	}))
	a.Torrents.Register(a.API)
	a.Sup.Add(a.Torrents, a.ModuleEnabled(ctx, a.Torrents.Name()))
}

// initMeta — кэш картинок (маршрут /img/{key}) и модуль ratings: рейтинги Кинопоиска по ключу из
// настроек kinopoisk.key (спека, разделы 8 и 15). Без ключа модуль работает: рейтинги по номеру —
// без ключа (rating.kinopoisk.ru), поиск ждёт ключа.
func (a *App) initMeta(ctx context.Context, o Options) error {
	images, err := meta.NewImages(meta.ImagesOptions{Dir: a.Paths.Images, Proxy: a.trackerProxy(ctx), Log: a.Log.With("module", "images")})
	if err != nil {
		return err
	}
	a.Images = images
	a.API.Handle("GET /img/{key}", "", images.Handler())
	a.kp = meta.NewKinopoisk(meta.KinopoiskOptions{Key: a.setting(ctx, "kinopoisk.key", ""), APIBase: o.KinopoiskAPI, RatingBase: o.KinopoiskAPI})
	a.Ratings = meta.NewRatings(meta.RatingsOptions{KP: a.kp, DB: a.DB, Log: a.Log.With("module", "ratings")})
	a.Sup.Add(a.Ratings, a.ModuleEnabled(ctx, a.Ratings.Name()))
	return nil
}

// initCatalog — источники, модуль edge и модуль catalog (спека, разделы 3, 6, 7). Настройки:
// proxy.trackers, rutracker.login, rutracker.password, catalog.categories («rutracker:2110,
// rutor:12»; пусто — разделы по умолчанию). Источник каждого трекера — один на процесс: предел
// «три поиска Rutor одновременно» и один ограничитель на трекер — на экземпляр.
func (a *App) initCatalog(ctx context.Context, o Options) error {
	proxy := a.trackerProxy(ctx)
	log := a.Log.With("module", "catalog")
	cats, err := catalog.ParseCategories(a.setting(ctx, "catalog.categories", ""))
	if err != nil {
		a.setProblem(ctx, "catalog.categories", "Разделы каталога в настройках не читаются — взяты разделы по умолчанию: "+err.Error())
		cats = catalog.DefaultCategories
	} else {
		a.clearProblem(ctx, "catalog.categories")
	}
	rutorSrc, err := rutor.New(rutor.Options{Proxy: proxy, Mirrors: o.Trackers.RutorMirrors,
		DownloadBase: o.Trackers.RutorDownload, Rate: o.Trackers.Rate, Log: log})
	if err != nil {
		return err
	}
	rto := rutracker.Options{Proxy: proxy, Mirrors: o.Trackers.RutrackerMirrors, APIBase: o.Trackers.RutrackerAPI,
		FeedBase: o.Trackers.RutrackerFeed, Rate: o.Trackers.Rate, Log: log,
		Login: a.setting(ctx, "rutracker.login", ""), Password: a.setting(ctx, "rutracker.password", "")}
	edgeOn := !o.Trackers.NoEdge && a.ModuleEnabled(ctx, "edge")
	if edgeOn {
		// UA — только начальный: Edge пересчитывает его на каждый проход, и источник переключается
		// на UA пропуска (Edge мог обновиться, пока служба работает, — этап 5a).
		if ua, err := edge.UserAgent(); err == nil {
			rto.UserAgent = ua
			rto.Passer = edge.New(edge.Options{ProfileDir: a.Paths.EdgeProfile, Proxy: proxy, Log: a.Log.With("module", "edge")})
		} else {
			log.Warn("Edge не найден — Rutracker только по API", "err", err)
		}
	}
	rtSrc, err := rutracker.New(rto)
	if err != nil {
		return err
	}
	a.Sup.Add(edge.NewModule(a.Log.With("module", "edge")), edgeOn)
	a.Catalog = catalog.New(catalog.Options{DB: a.DB, Sources: []source.Source{rutorSrc, rtSrc}, Categories: cats,
		Ratings: a.Ratings, Images: a.Images, KinopoiskPoster: a.kp.PosterURL, Log: log})
	a.Sup.Add(a.Catalog, a.ModuleEnabled(ctx, a.Catalog.Name()))
	return nil
}

// trackerProxy — прокси для трекеров из настроек; неверный — пусто (проблему proxy.invalid
// записывает initTorrents).
func (a *App) trackerProxy(ctx context.Context) string {
	proxy := a.setting(ctx, "proxy.trackers", "")
	if _, err := netx.ParseProxy(proxy); err != nil {
		return ""
	}
	return proxy
}

// ModuleEnabled — модуль включён, если в настройках нет modules.<имя>.enabled = "false".
func (a *App) ModuleEnabled(ctx context.Context, name string) bool {
	v, ok, err := a.DB.Setting(ctx, "modules."+name+".enabled")
	if err != nil || !ok {
		return true
	}
	return v != "false"
}

// setting — значение настройки или def, если её нет.
func (a *App) setting(ctx context.Context, key, def string) string {
	if v, ok := a.opts.Settings[key]; ok && v != "" {
		return v
	}
	v, ok, err := a.DB.Setting(ctx, key)
	if err != nil || !ok || v == "" {
		return def
	}
	return v
}

func (a *App) setProblem(ctx context.Context, id, text string) {
	if err := a.DB.SetProblem(ctx, id, text); err != nil {
		a.Log.Error("не удалось записать проблему", "id", id, "err", err)
	}
}

func (a *App) clearProblem(ctx context.Context, id string) {
	if err := a.DB.ClearProblem(ctx, id); err != nil {
		a.Log.Error("не удалось снять проблему", "id", id, "err", err)
	}
}

// Run работает до отмены ctx.
func (a *App) Run(ctx context.Context) {
	a.Log.Info("Kinodom запущен", "home", a.Paths.Home)
	a.Sup.Run(ctx)
	a.Log.Info("Kinodom остановлен")
}

func (a *App) Close() error {
	var errs []error
	for i := len(a.closers) - 1; i >= 0; i-- {
		errs = append(errs, a.closers[i].Close())
	}
	a.closers = nil
	return errors.Join(errs...)
}

type closerFunc func() error

func (f closerFunc) Close() error { return f() }
