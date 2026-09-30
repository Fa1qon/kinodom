// Package app собирает сервер из модулей. Его используют команда run, служба (этап 11)
// и интеграционные тесты — поэтому «все модули вместе» везде собираются одинаково.
package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"reflect"
	"strconv"
	"time"
	"unicode"

	"github.com/anacrolix/torrent/metainfo"
	"golang.org/x/time/rate"

	"kinodom/internal/api"
	"kinodom/internal/catalog"
	"kinodom/internal/config"
	"kinodom/internal/edge"
	"kinodom/internal/history"
	"kinodom/internal/httpx"
	"kinodom/internal/iptv"
	"kinodom/internal/library"
	"kinodom/internal/logx"
	"kinodom/internal/meta"
	"kinodom/internal/netx"
	"kinodom/internal/power"
	"kinodom/internal/settings"
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
	// LocalImages — тесты: картинки с адресов этого ПК (фейковые хостинги). В работе адреса этого ПК
	// и домашней сети в картинках не скачиваются.
	LocalImages bool
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
	Power    *power.Keeper     // запрет сна, пока идёт поток любого модуля (спека, раздел 9)
	Torrents *torrents.Service // nil, если движок не запустился (см. проблему torrents.engine)
	Ratings  *meta.Ratings     // рейтинги Кинопоиска (модуль ratings)
	Images   *meta.Images      // картинки, которые сервер отдаёт по /img/{key}
	Catalog  *catalog.Catalog  // каталог и поиск (модуль catalog)
	Settings *settings.Service // настройки из пульта: меняются без перезапуска (этап 7)
	IPTV     *iptv.Module      // каналы (модуль iptv, этап 8)
	History  *history.Service  // история просмотров по устройствам (этап 8c)
	Library  *library.Library  // медиатека: скачанное и папки заказчика (модуль library, этап 9)

	kp        *meta.Kinopoisk
	rutracker *rutracker.Rutracker
	proxy     *netx.Proxy // прокси для трекеров: один на всех, меняется в пульте на ходу
	closers   []io.Closer // закрываются в обратном порядке
}

func New(ctx context.Context, o Options) (*App, error) {
	home := o.Home
	if home == "" {
		home = config.DefaultHome()
	}
	a := &App{Paths: config.NewPaths(home)}
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
	vals, err := loadSettings(ctx, db, o)
	if err != nil {
		return fail(fmt.Errorf("настройки: %w", err))
	}
	a.Settings = settings.New(db, vals, a)
	a.proxy = a.initProxy(ctx, vals.Proxy)

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
	a.Settings.Register(a.API)

	a.Power = power.New(log.With("module", "power"))
	a.closers = append(a.closers, closerFunc(a.Power.Close))
	a.initTorrents(ctx, o, vals)
	if err := a.initMeta(ctx, o, vals); err != nil {
		return fail(err)
	}
	if err := a.initCatalog(ctx, o, vals); err != nil {
		return fail(err)
	}
	if err := a.initIPTV(ctx, o, vals); err != nil {
		return fail(err)
	}
	a.initLibrary(ctx)
	a.API.SetStatus(a.statusFields)
	// Следующие этапы добавляют сюда свои модули так же: a.Sup.Add(m, a.ModuleEnabled(ctx, m.Name())).
	return a, nil
}

// loadSettings — настройки из базы. Options.Settings и Options.DownloadsDir — поверх базы и в неё не
// пишутся (тесты, kinodom catalog, kinodom run --downloads).
func loadSettings(ctx context.Context, db *store.DB, o Options) (settings.Values, error) {
	overrides := maps.Clone(o.Settings)
	if overrides == nil {
		overrides = map[string]string{}
	}
	if o.DownloadsDir != "" {
		overrides[settings.KeyDownloadsDir] = o.DownloadsDir
	}
	return settings.Load(ctx, db, settings.Defaults{DownloadsDir: DefaultDownloadsDir,
		Sections: catalog.FormatCategories(catalog.DefaultCategories)}, overrides)
}

// initProxy — прокси для трекеров, общий для источников, картинок, Edge и анонсов. Неверный адрес в
// базе (записан до этапа 7) — работа напрямую и проблема в «Состоянии»: торренты работают, только
// анонсы Rutracker могут не пройти.
func (a *App) initProxy(ctx context.Context, s string) *netx.Proxy {
	px, err := netx.NewProxy(s)
	if err != nil {
		a.setProblem(ctx, "proxy.invalid", "Прокси в настройках не работает: "+err.Error())
		px, _ = netx.NewProxy("")
		return px
	}
	a.clearProblem(ctx, "proxy.invalid")
	return px
}

// initTorrents добавляет модуль торрентов. Движок создаётся внутри модуля: если папка
// загрузок недоступна, сервер работает без торрентов (проблема в «Состоянии», маршруты — 503),
// а сторож повторяет попытки, пока папка не появится или её не сменят в настройках.
func (a *App) initTorrents(ctx context.Context, o Options, v settings.Values) {
	port := a.Boot.TorrentPort
	if o.Offline {
		port = 0
	}
	log := a.Log.With("module", "torrents")
	cfg := torrents.Config{
		StateDir:    a.Paths.Torrent,
		ListenPort:  port,
		UploadLimit: uploadBytes(v),
		Proxy:       a.proxy,
		Offline:     o.Offline,
		Log:         log,
	}
	a.Torrents = torrents.NewLazyService(
		func() (*torrents.Engine, error) {
			// Папка — текущая из настроек: если прежняя была недоступна, её могли сменить в пульте.
			c := cfg
			c.DownloadsDir = a.Settings.Current().DownloadsDir
			return torrents.NewEngine(c)
		},
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
	a.Torrents.SetPolicy(policyOf(v))
	a.initHistory()
	a.Torrents.SetWatchTracker(a.History) // место по потоку — в историю устройства
	a.Torrents.UseKeeper(a.Power)
	a.Torrents.Register(a.API)
	a.API.Handle("GET /api/v1/downloads", a.Torrents.Name(), http.HandlerFunc(a.handleDownloads))
	a.Sup.Add(a.Torrents, a.ModuleEnabled(ctx, a.Torrents.Name()))
}

// initMeta — кэш картинок (маршрут /img/{key}) и модуль ratings: рейтинги Кинопоиска по ключу из
// настроек kinopoisk.key (спека, разделы 8 и 15). Без ключа модуль работает: рейтинги по номеру —
// без ключа (rating.kinopoisk.ru), поиск ждёт ключа.
func (a *App) initMeta(ctx context.Context, o Options, v settings.Values) error {
	images, err := meta.NewImages(meta.ImagesOptions{Dir: a.Paths.Images, Proxy: a.proxy, Log: a.Log.With("module", "images"),
		AllowPrivate: o.LocalImages})
	if err != nil {
		return err
	}
	a.Images = images
	a.API.Handle("GET /img/{key}", "", images.Handler())
	a.kp = meta.NewKinopoisk(meta.KinopoiskOptions{Key: v.KinopoiskKey, APIBase: o.KinopoiskAPI, RatingBase: o.KinopoiskAPI})
	a.Ratings = meta.NewRatings(meta.RatingsOptions{KP: a.kp, DB: a.DB, Log: a.Log.With("module", "ratings")})
	a.Sup.Add(a.Ratings, a.ModuleEnabled(ctx, a.Ratings.Name()))
	return nil
}

// initCatalog — источники, модуль edge и модуль catalog (спека, разделы 3, 6, 7). Настройки:
// proxy.trackers, rutracker.login, rutracker.password, catalog.categories («rutracker:2110,
// rutor:12»; пусто — разделы по умолчанию). Источник каждого трекера — один на процесс: предел
// «три поиска Rutor одновременно» и один ограничитель на трекер — на экземпляр.
func (a *App) initCatalog(ctx context.Context, o Options, v settings.Values) error {
	log := a.Log.With("module", "catalog")
	sections, err := catalog.ParseSections(v.Sections)
	if err != nil {
		a.setProblem(ctx, "catalog.categories", "Разделы каталога в настройках не читаются — взяты разделы по умолчанию: "+err.Error())
		sections = catalog.DefaultSections
	} else {
		a.clearProblem(ctx, "catalog.categories")
	}
	rutorSrc, err := rutor.New(rutor.Options{Proxy: a.proxy, Mirrors: o.Trackers.RutorMirrors,
		DownloadBase: o.Trackers.RutorDownload, Rate: o.Trackers.Rate, Log: log})
	if err != nil {
		return err
	}
	rto := rutracker.Options{Proxy: a.proxy, Mirrors: o.Trackers.RutrackerMirrors, APIBase: o.Trackers.RutrackerAPI,
		FeedBase: o.Trackers.RutrackerFeed, Rate: o.Trackers.Rate, Log: log,
		Login: v.RutrackerLogin, Password: v.RutrackerPassword, OnLogin: a.rutrackerLogin}
	edgeOn := !o.Trackers.NoEdge && a.ModuleEnabled(ctx, "edge")
	if edgeOn {
		// UA — только начальный: Edge пересчитывает его на каждый проход, и источник переключается
		// на UA пропуска (Edge мог обновиться, пока служба работает, — этап 5a).
		if ua, err := edge.UserAgent(); err == nil {
			rto.UserAgent = ua
			rto.Passer = edge.New(edge.Options{ProfileDir: a.Paths.EdgeProfile, Proxy: a.proxy, Log: a.Log.With("module", "edge")})
		} else {
			log.Warn("Edge не найден — Rutracker только по API", "err", err)
		}
	}
	rtSrc, err := rutracker.New(rto)
	if err != nil {
		return err
	}
	a.rutracker = rtSrc
	// Проблема входа прошлого запуска в базе: запрет входа живёт в памяти, после перезапуска его нет.
	a.rutrackerLogin(rtSrc.LoginState())
	// Кнопка «Войти» в настройках (спека этапа 7, раздел 5.3): из домашней сети.
	a.API.HandleHome("POST /api/v1/sources/rutracker/login", "catalog", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteJSON(w, http.StatusOK, rtSrc.Relogin(r.Context()))
	}))
	a.Sup.Add(edge.NewModule(a.Log.With("module", "edge")), edgeOn)
	a.Catalog = catalog.New(catalog.Options{DB: a.DB, Sources: []source.Source{rutorSrc, rtSrc}, Sections: sections,
		Ratings: a.Ratings, Images: a.Images, KinopoiskPoster: a.kp.PosterURL, TorrentFormat: torrentFormat,
		KeepImages: func(ctx context.Context) (map[string]bool, error) { // постеры медиатеки (этап 9)
			if a.Library == nil {
				return nil, nil
			}
			return a.Library.ImageKeys(ctx)
		},
		PreferredFormat: v.PreferredFormat, Log: log})
	a.Catalog.Register(a.API)
	a.API.Handle("GET /api/v1/releases/{id}", a.Catalog.Name(), http.HandlerFunc(a.handleRelease))
	a.API.Handle("POST /api/v1/releases/{id}/download", a.Torrents.Name(), http.HandlerFunc(a.handleDownload))
	a.Sup.Add(a.Catalog, a.ModuleEnabled(ctx, a.Catalog.Name()))
	return nil
}

// initIPTV — модуль iptv (спека этапа 8): плейлисты, каналы, проверки. Логотипы каналов — в своём кэше
// картинок (data\logos): кэш постеров чистит каталог. Телепрограмма, плейлисты и логотипы — напрямую.
func (a *App) initIPTV(ctx context.Context, o Options, v settings.Values) error {
	logos, err := meta.NewImages(meta.ImagesOptions{Dir: a.Paths.Logos, Rate: 20, Log: a.Log.With("module", "logos"),
		AllowPrivate: o.LocalImages})
	if err != nil {
		return err
	}
	a.IPTV = iptv.New(iptv.Options{DB: a.DB, Dir: a.Paths.IPTV, EPGURL: v.EPGURL, Hidden: hiddenOf(v),
		Location: iptv.Zone(v.UTCOffset), Log: a.Log.With("module", "iptv")})
	a.IPTV.Register(a.API, func(w http.ResponseWriter, r *http.Request, src string) {
		key, err := logos.Fetch(r.Context(), src, meta.Direct)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		logos.ServeKey(w, r, key, false)
	})
	a.Sup.Add(a.IPTV, a.ModuleEnabled(ctx, a.IPTV.Name()))
	return nil
}

// hiddenOf — скрытие каналов из настроек.
func hiddenOf(v settings.Values) iptv.Hidden {
	return iptv.Hidden{Categories: v.HiddenCategories, Countries: v.HiddenCountries, Languages: v.HiddenLanguages,
		OtherZones: v.HideOtherZones}
}

// rutrackerLogin — проблема rutracker.login, пока вход заблокирован (неверный пароль или капча):
// её видно баннером во вкладке Rutracker и в «Состоянии» (спека этапа 7, раздел 5.3).
func (a *App) rutrackerLogin(info rutracker.LoginInfo) {
	ctx := context.Background()
	if info.State != rutracker.LoginBlocked {
		a.clearProblem(ctx, "rutracker.login")
		return
	}
	text := []rune(info.Text)
	text[0] = unicode.ToLower(text[0])
	a.setProblem(ctx, "rutracker.login", "Rutracker: "+string(text))
}

// releaseView — раздача для экрана раздачи: описание — от каталога, список серий — от торрентов,
// если он известен до «Скачать» (спека этапа 7, раздел 5.4).
type releaseView struct {
	catalog.ReleaseView
	Files []torrents.FileInfo `json:"files"` // [] — неизвестен, пока раздачу не открыли
}

func (a *App) handleRelease(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "неверный номер раздачи")
		return
	}
	rel, err := a.Catalog.Release(r.Context(), id)
	switch {
	case errors.Is(err, catalog.ErrNoRelease):
		httpx.WriteError(w, http.StatusNotFound, err.Error())
		return
	case err != nil:
		httpx.WriteError(w, http.StatusInternalServerError, "раздача не читается: "+err.Error())
		return
	}
	out := releaseView{ReleaseView: rel.View(), Files: []torrents.FileInfo{}}
	var ih metainfo.Hash
	switch {
	case len(rel.Torrent) > 0: // Rutor: .torrent скачан заранее
		if fs, err := torrents.PlayableFiles(rel.Torrent); err == nil {
			out.Files = fs
		}
	case rel.InfoHash != "" && ih.FromHexString(rel.InfoHash) == nil:
		if fs, ok, err := a.Torrents.KnownFiles(r.Context(), ih); err == nil && ok {
			out.Files = fs
		}
	}
	if len(out.Files) > 0 { // файлы известны — формат по ним, а не по описанию (спека этапа 7, раздел 10.2)
		out.Format = meta.Format(metaFiles(out.Files))
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// torrentFormat — формат раздачи по видеофайлам .torrent (спека этапа 7, раздел 10.2).
func torrentFormat(b []byte) string {
	fs, err := torrents.PlayableFiles(b)
	if err != nil {
		return ""
	}
	return meta.Format(metaFiles(fs))
}

func metaFiles(fs []torrents.FileInfo) []meta.File {
	out := make([]meta.File, len(fs))
	for i, f := range fs {
		out[i] = meta.File{Name: f.Name, Size: f.Size}
	}
	return out
}

// handleDownload — «Скачать» (спека этапа 7, раздел 5.5): раздача из каталога открывается —
// Rutor из заранее скачанного .torrent, иначе по magnet — и все её видеофайлы (или один, {"file": N})
// встают в очередь загрузки. Работает с любого устройства: телевизор тоже нажимает «Скачать».
func (a *App) handleDownload(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "неверный номер раздачи")
		return
	}
	var req struct {
		File *int `json:"file"`
	}
	if !httpx.ReadJSON(w, r, &req) {
		return
	}
	rel, err := a.Catalog.Release(r.Context(), id)
	switch {
	case errors.Is(err, catalog.ErrNoRelease):
		httpx.WriteError(w, http.StatusNotFound, err.Error())
		return
	case err != nil:
		httpx.WriteError(w, http.StatusInternalServerError, "раздача не читается: "+err.Error())
		return
	case len(rel.Torrent) == 0 && rel.Magnet == "":
		httpx.WriteError(w, http.StatusConflict, "у раздачи ещё нет magnet-ссылки — страница раздачи догружается, попробуйте через минуту")
		return
	}
	ih, err := a.Torrents.Open(r.Context(), torrents.Source{Torrent: rel.Torrent, Magnet: rel.Magnet})
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	var files []int
	if req.File != nil {
		files = []int{*req.File}
	}
	switch err := a.Torrents.Download(r.Context(), ih, files); {
	case errors.Is(err, torrents.ErrLowSpace):
		httpx.WriteError(w, http.StatusInsufficientStorage, err.Error())
	case errors.Is(err, torrents.ErrNoSuchFile):
		httpx.WriteError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, torrents.ErrNoInfo):
		httpx.WriteError(w, http.StatusConflict, err.Error())
	case err != nil:
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
	default:
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"hash": ih.HexString()})
	}
}

// ModuleEnabled — модуль включён, если в настройках нет modules.<имя>.enabled = "false".
func (a *App) ModuleEnabled(ctx context.Context, name string) bool {
	v, ok, err := a.DB.Setting(ctx, "modules."+name+".enabled")
	if err != nil || !ok {
		return true
	}
	return v != "false"
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

// uploadBytes — лимит отдачи для движка, байт/с: пустое поле — без ограничения (0), 0 — не раздавать.
func uploadBytes(v settings.Values) float64 {
	switch {
	case v.UploadMBps == nil:
		return 0
	case *v.UploadMBps == 0:
		return torrents.NoUpload
	}
	return *v.UploadMBps * 1024 * 1024
}

// downloadItem — строка «Загрузок»: файл — от торрентов, название и постер раздачи — от каталога.
type downloadItem struct {
	torrents.DownloadItem
	Release *catalog.ReleaseRef `json:"release"` // null — раздача не из каталога
}

// handleDownloads — экран «Загрузки» (спека этапа 7, раздел 5.5).
func (a *App) handleDownloads(w http.ResponseWriter, r *http.Request) {
	v, err := a.Torrents.Downloads(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "загрузки не читаются: "+err.Error())
		return
	}
	hashes := make([]string, len(v.Items))
	for i, it := range v.Items {
		hashes[i] = it.Hash
	}
	refs, err := a.Catalog.ReleasesByHash(r.Context(), hashes)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "загрузки не читаются: "+err.Error())
		return
	}
	items := make([]downloadItem, len(v.Items))
	for i, it := range v.Items {
		items[i].DownloadItem = it
		if ref, ok := refs[it.Hash]; ok {
			items[i].Release = &ref
		}
	}
	httpx.WriteJSON(w, http.StatusOK, struct {
		torrents.DownloadsView
		Items []downloadItem `json:"items"`
	}{v, items})
}

// policyOf — правила хранения из настроек (основная спека, раздел 15).
func policyOf(v settings.Values) torrents.Policy {
	return torrents.Policy{KeepFor: time.Duration(v.KeepDays) * 24 * time.Hour, MinFree: int64(v.MinFreeGB) << 30,
		KeepBehind: v.KeepBehind}
}

func sameUpload(a, b *float64) bool {
	return (a == nil) == (b == nil) && (a == nil || *a == *b)
}

// Check — проверки настроек из пульта, которым нужны модули (settings.Applier). Ошибка — отказ,
// ничего не сохраняется.
func (a *App) Check(ctx context.Context, old, n settings.Values) error {
	if n.DownloadsDir != old.DownloadsDir {
		if err := torrents.CheckDownloadsDir(n.DownloadsDir); err != nil {
			return &settings.FieldError{Field: "Папка загрузок", Text: err.Error()}
		}
	}
	if n.Sections != old.Sections {
		ss, err := catalog.ParseSections(n.Sections)
		if err == nil {
			err = a.Catalog.CheckSections(ctx, ss)
		}
		if err != nil {
			return &settings.FieldError{Field: "Разделы каталога", Text: err.Error()}
		}
	}
	return nil
}

// Apply — сохранённые настройки из пульта к работающим модулям, без перезапуска (settings.Applier).
func (a *App) Apply(ctx context.Context, old, n settings.Values) {
	if n.KinopoiskKey != old.KinopoiskKey {
		a.kp.SetKey(n.KinopoiskKey)
		a.Ratings.KeyChanged(ctx)
	}
	if n.KeepDays != old.KeepDays || n.MinFreeGB != old.MinFreeGB || n.KeepBehind != old.KeepBehind {
		a.Torrents.SetPolicy(policyOf(n))
	}
	if !sameUpload(n.UploadMBps, old.UploadMBps) {
		a.Torrents.SetUploadLimit(uploadBytes(n))
	}
	if n.DownloadsDir != old.DownloadsDir {
		if e := a.Torrents.Engine(); e != nil {
			e.SetDownloadsDir(n.DownloadsDir)
		}
	}
	if n.PreferredFormat != old.PreferredFormat {
		a.Catalog.SetPreferredFormat(n.PreferredFormat)
	}
	if n.Sections != old.Sections {
		ss, _ := catalog.ParseSections(n.Sections) // проверено в Check
		if err := a.Catalog.SetSections(ctx, ss); err != nil {
			a.Log.Error("разделы каталога не применились", "err", err)
		} else {
			a.clearProblem(ctx, "catalog.categories")
		}
	}
	if n.Proxy != old.Proxy {
		// Адрес уже проверен settings.Values.With: Set не откажет.
		if err := a.proxy.Set(n.Proxy); err == nil {
			a.clearProblem(ctx, "proxy.invalid")
		}
	}
	if n.EPGURL != old.EPGURL {
		a.IPTV.SetEPGURL(n.EPGURL)
	}
	if !reflect.DeepEqual(hiddenOf(n), hiddenOf(old)) {
		a.IPTV.SetHidden(hiddenOf(n))
	}
	if n.UTCOffset != old.UTCOffset {
		a.IPTV.SetLocation(iptv.Zone(n.UTCOffset))
	}
	if n.RutrackerLogin != old.RutrackerLogin || n.RutrackerPassword != old.RutrackerPassword {
		a.rutracker.SetCredentials(n.RutrackerLogin, n.RutrackerPassword)
	}
	a.Log.Info("настройки изменены в пульте") // без значений: среди них пароли и ключ
}
