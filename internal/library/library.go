package library

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"kinodom/internal/history"
	"kinodom/internal/meta"
	"kinodom/internal/power"
	"kinodom/internal/store"
	"kinodom/internal/supervisor"
	"kinodom/internal/watch"
)

// Ratings — рейтинги Кинопоиска по номерам (meta.Ratings).
type Ratings interface {
	Films(ctx context.Context, ids []int) (map[int]meta.Rating, error)
	AddFilm(ctx context.Context, f meta.Film) error
}

// Posters — кэш картинок (meta.Images): постер Кинопоиска скачивается один раз.
type Posters interface {
	Fetch(ctx context.Context, src string, via meta.Via) (string, error)
}

// TorrentFile — видеофайл скачанной раздачи.
type TorrentFile struct {
	Index     int
	Path      string // путь внутри раздачи через «/»
	Size      int64
	Done      int64
	Stored    bool   // хранится и докачивается
	Readiness string // цвет готовности «Смотреть» (torrents.Readiness)
}

// ReleaseData — раздача в каталоге: номер Кинопоиска и данные карточки со страницы раздачи.
type ReleaseData struct {
	KP          int
	Title       string // название раздачи целиком
	NameRu      string
	NameOrig    string
	Description string
	ImageKey    string
	Quality     string
	Year        int
}

// TorrentUnit — скачанная раздача с хотя бы одним хранимым файлом.
type TorrentUnit struct {
	Hash       string // infohash, нижний регистр
	Name       string
	Dir        string // папка раздачи на диске
	Files      []TorrentFile
	LastOpened time.Time    // последнее открытие любого файла; нулевое — ни разу
	Release    *ReleaseData // nil — раздачи нет в каталоге
	// Missing — у раздачи есть хранимые файлы, но она ещё не загружена (старт, отключённый диск):
	// единица остаётся, но не показывается.
	Missing bool
}

// Downloads — скачанное (адаптер приложения над torrents и catalog).
type Downloads interface {
	TorrentUnits(ctx context.Context) ([]TorrentUnit, error)
}

// History — история просмотров по устройствам (history.Service): места, «продолжить», длительность;
// Report — место по чтению потока (watch).
type History interface {
	Files(ctx context.Context, device, hash string) ([]history.FileProgress, error)
	List(ctx context.Context, device string) ([]history.Item, error)
	StartSec(ctx context.Context, device, hash string, index int) int
	Report(ctx context.Context, device, hash string, index int, offset, size int64)
	Duration(ctx context.Context, hash string, index int) (float64, bool)
	SetDuration(ctx context.Context, hash string, index int, sec float64) error
}

type Options struct {
	DB           *store.DB
	KP           KP // nil — без Кинопоиска
	Ratings      Ratings
	Posters      Posters
	KPPoster     func(id int) string // постер Кинопоиска по номеру — повтор, когда постер карточки не скачался (Х9)
	Downloads    Downloads           // nil — без скачанного
	History      History
	Power        *power.Keeper
	DownloadsDir func() string // папка загрузок: её нельзя добавить в категорию
	KeepDays     func() int    // срок хранения скачанного (torrents.keepDays)
	// TorrentFolders — папки раздач Kinodom (torrents.Service.Folders): обход их пропускает — скачанное в
	// папку медиатеки приходит единицей раздачи (план 14В). nil — нет.
	TorrentFolders func(ctx context.Context) ([]string, error)
	// Writable — служба может писать в папку (папки «Фильмов» и «Сериалов» — для скачанного); nil — проба файлом.
	Writable func(dir string) bool
	Log      *slog.Logger
	Now      func() time.Time
}

// Library — модуль «library».
type Library struct {
	o   Options
	d   db
	log *slog.Logger
	now func() time.Time

	mu       sync.Mutex
	runCtx   context.Context
	scanning bool
	rescan   bool // во время обхода попросили обязательный — ещё один после (Х17)
	// dlRetry — загрузки не прочитались (движок ещё не поднялся после старта): повтор обхода в это
	// время, а не через час (хвост Х21); ноль — не нужен.
	dlRetry  time.Time
	lastScan time.Time
	problems map[int64]string // папка категории → not_found, no_access
	kpPause  time.Time        // квота Кинопоиска кончилась — распознавание не раньше
	retries  map[string]retry // «details:<номер>», «poster:<номер>» → повтор после сбоя (Х9)
	recWake  chan struct{}    // разбудить цикл распознавания (после обхода)

	tracker  *watch.Tracker                         // место по чтению потока файлов из папок
	durTried sync.Map                               // «раздача/номер» → длительность уже пробовали узнать
	open     func(name string) (mediaReader, error) // nil — os.Open; тесты обрывают чтение
}

// ScanState — идёт ли обход и когда был последний.
type ScanState struct {
	Running bool      `json:"running"`
	LastAt  time.Time `json:"lastAt"`
}

// ErrNoCard — карточки нет или она в скрытой на этом устройстве категории.
var ErrNoCard = errors.New("такой карточки в медиатеке нет")

// Пределы обхода (спека, раздел 5.2).
var (
	scanEvery    = time.Hour
	scanMinGap   = time.Minute   // обход по открытию медиатеки — не чаще
	kpPauseAfter = time.Hour     // квота кончилась — следующая попытка
	retryAfter   = 6 * time.Hour // Кинопоиск сбоит на единице — её повтор
	maxAttempts  = 3             // столько сбоев подряд — «Не распознано»
)

func New(o Options) *Library {
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.DownloadsDir == nil {
		o.DownloadsDir = func() string { return "" }
	}
	if o.KeepDays == nil {
		o.KeepDays = func() int { return 14 }
	}
	l := &Library{o: o, d: db{o.DB}, log: o.Log, now: o.Now, problems: map[int64]string{}, retries: map[string]retry{}, recWake: make(chan struct{}, 1)}
	var rep watch.Reporter
	if o.History != nil {
		rep = mediaReporter{o.History}
	}
	l.tracker = watch.New(rep, l.now)
	return l
}

func (l *Library) Name() string { return "library" }

// Run — обход при старте и раз в час; обход по запросу пульта — Scan.
func (l *Library) Run(ctx context.Context) error {
	l.start(ctx)
	go l.recognizeLoop(ctx)
	supervisor.Ready(ctx)
	l.startScan(true)
	t := time.NewTicker(scanEvery)
	defer t.Stop()
	sec := time.NewTicker(time.Second)
	defer sec.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			l.startScan(true)
		case <-sec.C:
			l.tracker.Tick(l.now())
			l.retryDownloads(l.now())
		}
	}
}

func (l *Library) start(ctx context.Context) {
	l.mu.Lock()
	l.runCtx = ctx
	l.mu.Unlock()
}

// downloadsRetry — через сколько повторить обход, если загрузки не прочитались (хвост Х21).
const downloadsRetry = 30 * time.Second

// retryDownloads — пора повторить обход после неудачи чтения загрузок.
func (l *Library) retryDownloads(now time.Time) {
	l.mu.Lock()
	due := !l.dlRetry.IsZero() && !now.Before(l.dlRetry)
	l.mu.Unlock()
	if due {
		l.startScan(true)
	}
}

// Scan — обход папок в фоне по открытию медиатеки: не чаще раза в минуту, один за раз.
func (l *Library) Scan() ScanState { return l.startScan(false) }

// startScan — обход в фоне; force — обязательный (часовой, смена папок): если обход уже идёт, после него
// будет ещё один — иначе новая папка из настроек появилась бы только через час (хвост Х17).
func (l *Library) startScan(force bool) ScanState {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.runCtx == nil || l.runCtx.Err() != nil {
		return ScanState{Running: l.scanning, LastAt: l.lastScan}
	}
	if l.scanning {
		l.rescan = l.rescan || force
		return ScanState{Running: true, LastAt: l.lastScan}
	}
	if !force && l.now().Sub(l.lastScan) < scanMinGap {
		return ScanState{Running: false, LastAt: l.lastScan}
	}
	l.scanning = true
	ctx := l.runCtx
	go func() {
		for {
			l.scanOnce(ctx)
			l.mu.Lock()
			l.lastScan = l.now()
			if l.rescan && ctx.Err() == nil {
				l.rescan = false
				l.mu.Unlock()
				continue
			}
			l.scanning, l.rescan = false, false
			l.mu.Unlock()
			return
		}
	}()
	return ScanState{Running: true, LastAt: l.lastScan}
}

// scanOnce — один обход; паника не роняет сервер вместе с идущими фильмами.
func (l *Library) scanOnce(ctx context.Context) {
	defer func() {
		if p := recover(); p != nil {
			l.log.Error("медиатека: сбой обхода", "panic", fmt.Sprint(p), "stack", string(debug.Stack()))
		}
	}()
	if err := l.scanNow(ctx); err != nil && ctx.Err() == nil {
		l.log.Warn("медиатека: обход не удался", "err", err)
	}
}

func (l *Library) scanState() ScanState {
	l.mu.Lock()
	defer l.mu.Unlock()
	return ScanState{Running: l.scanning, LastAt: l.lastScan}
}

// scanNow — обход папок и скачанного, распознавание новых единиц, данные карточек.
func (l *Library) scanNow(ctx context.Context) error {
	cats, err := l.d.categories(ctx)
	if err != nil {
		return err
	}
	folders := map[string]int64{}
	for _, c := range cats {
		for _, f := range c.Folders {
			folders[pathKey(f.Path)] = f.ID
		}
	}
	problems := map[int64]string{}
	now := l.now()
	torrentDirs := map[string]bool{}
	if l.o.TorrentFolders != nil {
		ps, err := l.o.TorrentFolders(ctx)
		if err != nil {
			l.log.Warn("медиатека: папки раздач не прочитались", "err", err)
		}
		for _, p := range ps {
			torrentDirs[pathKey(p)] = true
		}
	}
	for _, c := range cats {
		for _, f := range c.Folders {
			dl := l.o.DownloadsDir()
			skip := func(p string) bool {
				if dl != "" && pathKey(p) == pathKey(dl) { // скачанное и так в медиатеке, недокачанное — пустое
					return true
				}
				if torrentDirs[pathKey(p)] { // раздача Kinodom в папке медиатеки — единица скачанного (план 14В)
					return true
				}
				id, ok := folders[pathKey(p)]
				return ok && id != f.ID
			}
			units, err := Scan(f.Path, c.Layout, skip, now)
			if err != nil {
				problems[f.ID] = "not_found"
				if errors.Is(err, ErrFolderAccess) {
					problems[f.ID] = "no_access"
				}
				if err := l.d.markMissing(ctx, f.ID); err != nil {
					return err
				}
				continue
			}
			if err := l.d.syncFolder(ctx, f.ID, c, units, now); err != nil {
				return err
			}
			if (c.Builtin == "films" || c.Builtin == "series") && !l.Writable(f.Path) {
				problems[f.ID] = "no_write" // сюда качается скачанное (план 14В)
			}
		}
	}
	l.mu.Lock()
	l.problems = problems
	l.mu.Unlock()
	if err := l.syncProblems(ctx, cats, problems); err != nil {
		return err
	}
	tus, err := l.torrents(ctx)
	l.mu.Lock()
	warned := !l.dlRetry.IsZero()
	if err != nil {
		l.dlRetry = now.Add(downloadsRetry)
	} else {
		l.dlRetry = time.Time{}
	}
	l.mu.Unlock()
	if err != nil {
		if !warned { // при старте движок поднимается за секунды — предупреждение один раз
			l.log.Warn("медиатека: скачанное не читается — обход повторится через 30 с", "err", err)
		}
	} else if err := l.d.syncTorrents(ctx, tus, now); err != nil {
		return err
	}
	// Распознавание и описания Кинопоиска — своим циклом (recognizeLoop): без токена запросы идут не чаще
	// раза в 3 с и после каталога, обход их не ждёт (11b-Б, найдено вживую).
	if err := l.refreshCards(ctx, tus, 0, true); err != nil {
		return err
	}
	l.wakeRecognizer()
	return nil
}

// recognizeEvery — цикл распознавания заглядывает и сам: повторы по паузе (Х9), ожидавшие квоту.
const recognizeEvery = 10 * time.Minute

func (l *Library) wakeRecognizer() {
	select {
	case l.recWake <- struct{}{}:
	default:
	}
}

// recognizeLoop — распознавание новых единиц и описания карточек с Кинопоиска: после каждого обхода и раз
// в recognizeEvery.
func (l *Library) recognizeLoop(ctx context.Context) {
	t := time.NewTicker(recognizeEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-l.recWake:
		case <-t.C:
		}
		l.recognizeOnce(ctx)
	}
}

// recognizeOnce — один проход распознавания; паника и ошибки не роняют модуль.
func (l *Library) recognizeOnce(ctx context.Context) {
	defer func() {
		if p := recover(); p != nil {
			l.log.Error("медиатека: сбой распознавания", "panic", fmt.Sprint(p), "stack", string(debug.Stack()))
		}
	}()
	tus, err := l.torrents(ctx)
	if err != nil {
		tus = nil
	}
	if err := l.recognizePending(ctx, tus, 0); err != nil {
		if ctx.Err() == nil {
			l.log.Warn("медиатека: распознавание не удалось", "err", err)
		}
		return
	}
	if err := l.refreshCards(ctx, tus, 0, false); err != nil && ctx.Err() == nil {
		l.log.Warn("медиатека: описания Кинопоиска не загрузились", "err", err)
	}
}

// syncProblems — недоступные папки категорий — проблемы в «Состоянии» (спека, раздел 5.11);
// вернувшиеся и убранные папки — проблемы сняты.
func (l *Library) syncProblems(ctx context.Context, cats []Category, problems map[int64]string) error {
	want := map[string]string{}
	for _, c := range cats {
		for _, f := range c.Folders {
			switch problems[f.ID] {
			case "not_found":
				want[problemID(f.ID)] = "Папка медиатеки не найдена: " + f.Path + " (категория «" + c.Name + "»)"
			case "no_access":
				want[problemID(f.ID)] = "Папка медиатеки не читается — нет прав: " + f.Path + " (категория «" + c.Name + "»)"
			case "no_write":
				want[problemID(f.ID)] = "Нет права записи в папку медиатеки: " + f.Path + " (категория «" + c.Name + "») — скачанное идёт в папку загрузок"
			}
		}
	}
	have, err := l.d.Problems(ctx)
	if err != nil {
		return err
	}
	for _, p := range have {
		if _, ok := want[p.ID]; !ok && strings.HasPrefix(p.ID, "library.folder.") {
			if err := l.d.ClearProblem(ctx, p.ID); err != nil {
				return err
			}
		}
	}
	for id, text := range want {
		if err := l.d.SetProblem(ctx, id, text); err != nil {
			return err
		}
	}
	return nil
}

func problemID(folder int64) string { return "library.folder." + strconv.FormatInt(folder, 10) }

// torrents — скачанное сейчас; без модуля загрузок — пусто.
func (l *Library) torrents(ctx context.Context) ([]TorrentUnit, error) {
	if l.o.Downloads == nil {
		return nil, nil
	}
	return l.o.Downloads.TorrentUnits(ctx)
}

// FolderProblems — недоступные папки категорий после последнего обхода: для «Состояния».
func (l *Library) FolderProblems() map[int64]string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make(map[int64]string, len(l.problems))
	for k, v := range l.problems {
		out[k] = v
	}
	return out
}

// Categories — категории с папками, их проблемами и флажком «показывать на этом устройстве».
func (l *Library) Categories(ctx context.Context, device string) ([]Category, error) {
	cs, err := l.d.categories(ctx)
	if err != nil {
		return nil, err
	}
	on, err := l.d.deviceCategories(ctx, device)
	if err != nil {
		return nil, err
	}
	problems := l.FolderProblems()
	for i := range cs {
		cs[i].OnDevice = on[cs[i].ID]
		for j := range cs[i].Folders {
			cs[i].Folders[j].Problem = problems[cs[i].Folders[j].ID]
		}
	}
	return cs, nil
}
