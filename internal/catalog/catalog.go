// Package catalog — модуль «catalog»: основной каталог по раздающим из топов включённых
// разделов, догрузка страниц раздач, постеров, .torrent и рейтингов в порядке каталога, поиск
// с частичными результатами (спека, раздел 7).
package catalog

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"kinodom/internal/meta"
	"kinodom/internal/netx"
	"kinodom/internal/source"
	"kinodom/internal/store"
	"kinodom/internal/supervisor"
)

// Расписание (спека, раздел 7).
const (
	refreshEvery = 6 * time.Hour    // раздел старше — обновить
	checkEvery   = 10 * time.Minute // как часто проверять, не пора ли
	staleAfter   = 48 * time.Hour   // каталог не обновлялся — проблема в «Состоянии»
)

// retryDelays — после неудачи: 1, 5, 15 минут, дальше каждые 15 до успеха.
var retryDelays = []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute}

// CategoryRef — раздел трекера в каталоге: «rutracker:2110».
type CategoryRef struct {
	Tracker string
	ID      string
}

func (c CategoryRef) String() string { return c.Tracker + ":" + c.ID }

// DefaultCategories — категории по умолчанию (спека, раздел 6).
var DefaultCategories = []CategoryRef{
	{"rutracker", "2110"}, {"rutracker", "2164"}, {"rutracker", "2166"}, {"rutracker", "2169"}, {"rutracker", "979"},
	{"rutracker", "56"}, {"rutracker", "2076"}, {"rutracker", "249"}, {"rutracker", "552"}, {"rutracker", "500"},
	{"rutor", "12"},
}

// FormatCategories — список разделов строкой настройки catalog.categories.
func FormatCategories(cs []CategoryRef) string {
	parts := make([]string, len(cs))
	for i, c := range cs {
		parts[i] = c.String()
	}
	return strings.Join(parts, ",")
}

type Options struct {
	DB       *store.DB
	Sources  []source.Source // по одному на трекер
	Sections []Section       // выбранные разделы; пусто — DefaultSections
	Ratings  *meta.Ratings   // nil — без рейтингов
	Images   *meta.Images    // nil — без картинок
	// KinopoiskPoster — адрес постера Кинопоиска (meta.Kinopoisk.PosterURL): когда на странице
	// раздачи картинки нет или хостинг не отвечает (спека, раздел 8). nil — без запасного постера.
	KinopoiskPoster func(id int) string
	// TorrentFormat — формат раздачи по видеофайлам .torrent (приложение: torrents.PlayableFiles и
	// meta.Format). nil — формат только по описанию.
	TorrentFormat   func(torrent []byte) string
	PreferredFormat string // формат в приоритете (catalog.preferredFormat); "" — нет
	// KeepImages — картинки других модулей в том же кэше (постеры медиатеки, этап 9): чистка их не
	// трогает. nil — только картинки каталога.
	KeepImages func(ctx context.Context) (map[string]bool, error)
	Log        *slog.Logger // nil — без журнала
}

// Catalog — модуль «catalog».
type Catalog struct {
	st         catalogStore
	db         *store.DB
	sources    map[string]source.Source
	ratings    *meta.Ratings
	images     *meta.Images
	kpPoster   func(id int) string
	keepImages func(ctx context.Context) (map[string]bool, error)
	log        *slog.Logger
	now        func() time.Time

	torrentFormat func(torrent []byte) string // формат по .torrent; nil — только по описанию

	refreshNow      chan struct{}
	sectionsChanged chan struct{} // разделы сменили в пульте: пройти по разделам без ожидания
	enrichWake      map[string]chan struct{}
	postersWake     chan struct{}

	mu          sync.Mutex
	sections    []Section            // разделы из настроек
	cats        []CategoryRef        // они же после раскрытия «+» по дереву (enabled)
	urgent      map[string][]int64   // трекер → раздачи, которые открыли в пульте: догрузить первыми
	posterTried map[int64]time.Time  // постер Кинопоиска не скачался — когда пробовали
	failures    int                  // неудачных проходов подряд
	forumPaused map[string]time.Time // трекер → до какого времени не ходить за страницами раздач
	runCtx      context.Context      // для фонового поиска: живёт, пока работает модуль
	searches    map[string]*searchRun
	preferred   string // формат в приоритете
}

func New(o Options) *Catalog {
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if len(o.Sections) == 0 {
		o.Sections = DefaultSections
	}
	c := &Catalog{st: catalogStore{o.DB}, db: o.DB, sources: map[string]source.Source{}, sections: o.Sections,
		ratings: o.Ratings, images: o.Images, kpPoster: o.KinopoiskPoster, keepImages: o.KeepImages, log: o.Log, now: time.Now,
		refreshNow: make(chan struct{}, 1), sectionsChanged: make(chan struct{}, 1), enrichWake: map[string]chan struct{}{},
		postersWake: make(chan struct{}, 1), urgent: map[string][]int64{}, posterTried: map[int64]time.Time{},
		forumPaused: map[string]time.Time{}, searches: map[string]*searchRun{}, torrentFormat: o.TorrentFormat,
		preferred: o.PreferredFormat}
	// До первого прохода (там дерево и раскрытие «+») — разделы как записаны, без подразделов.
	for _, s := range o.Sections {
		if !strings.HasPrefix(s.ID, "c") {
			c.cats = append(c.cats, CategoryRef{s.Tracker, s.ID})
		}
	}
	for _, s := range o.Sources {
		c.sources[s.Name()] = s
		c.enrichWake[s.Name()] = make(chan struct{}, 1)
	}
	return c
}

func (c *Catalog) Name() string { return "catalog" }

// Refresh — «Обновить сейчас» (спека, раздел 7): все разделы, не дожидаясь шести часов.
func (c *Catalog) Refresh() {
	select {
	case c.refreshNow <- struct{}{}:
	default:
	}
}

// Run — обновление топов по расписанию и догрузка раздач (по горутине на трекер).
func (c *Catalog) Run(ctx context.Context) error {
	c.mu.Lock()
	c.runCtx = ctx
	c.mu.Unlock()
	for name := range c.sources {
		supervisor.Go(ctx, func(ctx context.Context) error { return c.enrichLoop(ctx, name) })
	}
	supervisor.Go(ctx, c.imagesLoop)
	supervisor.Go(ctx, c.postersLoop)
	supervisor.Go(ctx, c.fillFormats)
	supervisor.Ready(ctx)
	force := false
	for {
		wait, err := c.refreshPass(ctx, force)
		if err != nil {
			return err // база — сбой модуля
		}
		force = false
		select {
		case <-ctx.Done():
			return nil
		case <-c.refreshNow:
			force = true
		case <-c.sectionsChanged:
		case <-time.After(wait):
		}
	}
}

// refreshPass обновляет разделы, чьё последнее удачное обновление старше шести часов (или все —
// force). Возвращает, через сколько заглянуть снова: 10 минут, а после неудачи — 1, 5, 15 минут,
// дальше каждые 15 (спека, раздел 7: так каталог переживает сон ПК и VPN, поднявшийся позже).
func (c *Catalog) refreshPass(ctx context.Context, force bool) (time.Duration, error) {
	now := c.now()
	trackerErr := map[string]error{}
	touched, failed := false, false
	for name, src := range c.sources {
		if err := c.refreshTree(ctx, name, src); err != nil && ctx.Err() == nil {
			c.log.Warn("каталог: разделы трекера не обновились", "tracker", name, "err", err)
		}
	}
	// Дерево могло измениться: у раздела с «+» появился подраздел — он войдёт в каталог сам.
	if err := c.reexpand(ctx); err != nil {
		return 0, dbError{err}
	}
	cats := c.enabled()
	for _, cat := range cats {
		src, ok := c.sources[cat.Tracker]
		if !ok {
			continue
		}
		if trackerDown(trackerErr[cat.Tracker]) {
			continue // трекер или прокси в этом проходе уже не ответили: у каждого раздела — таймауты по зеркалам
		}
		last, at, err := c.st.state(ctx, cat)
		if err != nil {
			return 0, err
		}
		if !force && now.Sub(at) < refreshEvery {
			continue
		}
		touched = true
		if err := c.refreshCategory(ctx, cat, src, last, now); err != nil {
			if ctx.Err() != nil {
				return 0, nil
			}
			if isDBError(err) {
				return 0, err
			}
			failed = true
			if _, kept := err.(keptError); !kept && trackerErr[cat.Tracker] == nil {
				trackerErr[cat.Tracker] = err // трекер не ответил; «пришло мало» — проблема раздела, не трекера
			}
		}
	}
	for name := range c.sources {
		if err := trackerErr[name]; err != nil {
			c.setProblem(ctx, "catalog."+name, err.Error())
		} else if touched {
			c.clearProblem(ctx, "catalog."+name)
		}
	}
	if err := c.checkStale(ctx, cats, now, trackerErr); err != nil {
		return 0, err
	}
	if touched {
		if err := c.enqueueRatings(ctx); err != nil {
			return 0, err
		}
		for _, ch := range c.enrichWake {
			select {
			case ch <- struct{}{}:
			default:
			}
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !failed {
		c.failures = 0
		return checkEvery, nil
	}
	c.failures++
	return retryDelays[min(c.failures, len(retryDelays))-1], nil
}

// refreshCategory — топ одного раздела (спека, раздел 7, «Обновление категории»).
func (c *Catalog) refreshCategory(ctx context.Context, cat CategoryRef, src source.Source, last int, now time.Time) error {
	rs, err := src.Top(ctx, cat.ID, 100)
	if err != nil {
		return err
	}
	problem := "catalog." + cat.String()
	name := c.st.categoryName(ctx, cat.Tracker, cat.ID)
	switch {
	case last > 0 && len(rs)*10 < last*3:
		// Пришло меньше 30 % от прошлого раза — позиции не заменяются.
		err := keptError{fmt.Sprintf("%s: в разделе «%s» пришло %d раздач вместо %d", title(cat.Tracker), name, len(rs), last)}
		c.setProblem(ctx, problem, err.Error())
		return err
	case brokenNumbers(rs):
		// Переименованное поле на странице дало бы нули без ошибки разбора (хвост этапа 3).
		err := keptError{fmt.Sprintf("%s: в разделе «%s» у всех раздач нет раздающих или размера — похоже, трекер изменил разметку", title(cat.Tracker), name)}
		c.setProblem(ctx, problem, err.Error())
		return err
	}
	if err := c.st.replaceTop(ctx, cat, rs, now); err != nil {
		return dbError{err}
	}
	c.clearProblem(ctx, problem)
	return nil
}

// brokenNumbers — у всех раздач нет раздающих или у всех нет размера: в топе по раздающим так не
// бывает, значит, переименовано поле (пустой топ — не повод).
func brokenNumbers(rs []source.Release) bool {
	if len(rs) == 0 {
		return false
	}
	noSeeders, noSize := true, true
	for _, r := range rs {
		noSeeders = noSeeders && r.Seeders == 0
		noSize = noSize && r.Size == 0
	}
	return noSeeders || noSize
}

// trackerDown — трекер или прокси не отвечают (а не «пришло мало» и не сломанный разбор).
func trackerDown(err error) bool {
	return errors.Is(err, netx.ErrTrackerDown) || errors.Is(err, netx.ErrProxyDown)
}

// refreshTree — дерево разделов трекера: для названий в каталоге и настроек. У Rutracker оно из
// API и кэшируется источником на сутки, у Rutor — постоянное.
func (c *Catalog) refreshTree(ctx context.Context, name string, src source.Source) error {
	cs, err := src.Categories(ctx)
	if err != nil {
		return err
	}
	if err := c.st.replaceCategories(ctx, name, cs); err != nil {
		return dbError{err}
	}
	return nil
}

// checkStale — «Каталог не обновлялся N дней» (спека, раздел 13), пока хоть один включённый
// раздел не обновлялся дольше двух суток.
func (c *Catalog) checkStale(ctx context.Context, cats []CategoryRef, now time.Time, trackerErr map[string]error) error {
	var oldest time.Time
	var reason error
	for _, cat := range cats {
		if _, ok := c.sources[cat.Tracker]; !ok {
			continue
		}
		_, at, err := c.st.state(ctx, cat)
		if err != nil {
			return err
		}
		if !at.IsZero() && now.Sub(at) > staleAfter && (oldest.IsZero() || at.Before(oldest)) {
			oldest, reason = at, trackerErr[cat.Tracker]
		}
	}
	if oldest.IsZero() {
		c.clearProblem(ctx, "catalog.stale")
		return nil
	}
	text := fmt.Sprintf("Каталог не обновлялся %d дн.", int(now.Sub(oldest).Hours()/24))
	if reason != nil {
		text += ": " + reason.Error()
	}
	c.setProblem(ctx, "catalog.stale", text)
	return nil
}

// enqueueRatings ставит раздачи каталога в очередь рейтингов в порядке каталога — только те, у
// кого уже есть страница раздачи: без неё нет ни номера Кинопоиска, ни названия у Rutracker.
func (c *Catalog) enqueueRatings(ctx context.Context) error {
	if c.ratings == nil {
		return nil
	}
	rs, err := c.st.catalogRows(ctx, c.enabled())
	if err != nil {
		return err
	}
	var items []meta.Item
	for _, r := range rs {
		if !r.DetailsAt.IsZero() {
			items = append(items, ratingItem(r))
		}
	}
	return c.ratings.EnqueueCatalog(ctx, items)
}

func ratingItem(r row) meta.Item {
	return meta.Item{Release: r.Tracker + ":" + r.TopicID, KinopoiskID: r.KinopoiskID, IMDbID: r.IMDbID, Title: r.Title}
}

func title(tracker string) string {
	switch tracker {
	case "rutor":
		return "Rutor"
	case "rutracker":
		return "Rutracker"
	}
	return tracker
}

func (c *Catalog) setProblem(ctx context.Context, id, text string) {
	if err := c.db.SetProblem(ctx, id, text); err != nil {
		c.log.Error("не удалось записать проблему", "id", id, "err", err)
	}
}

func (c *Catalog) clearProblem(ctx context.Context, id string) {
	if err := c.db.ClearProblem(ctx, id); err != nil {
		c.log.Error("не удалось снять проблему", "id", id, "err", err)
	}
}

// keptError — топ пришёл, но подозрительный: позиции раздела оставлены прежними, повтор — по
// расписанию неудач.
type keptError struct{ text string }

func (e keptError) Error() string { return e.text }

// dbError — ошибка базы: сбой модуля, а не трекера.
type dbError struct{ err error }

func (e dbError) Error() string { return "база каталога: " + e.err.Error() }
func (e dbError) Unwrap() error { return e.err }

func isDBError(err error) bool {
	_, ok := err.(dbError)
	return ok
}

// imagesKeepFor — картинки раздач, которых нет в каталоге и которых не трогали столько, уходят
// из кэша.
const imagesKeepFor = 30 * 24 * time.Hour

// imagesLoop — раз в сутки (первый раз — через 10 минут после старта) чистит кэш картинок.
func (c *Catalog) imagesLoop(ctx context.Context) error {
	wait := 10 * time.Minute
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(wait):
		}
		if err := c.sweepImages(ctx); err != nil {
			return err
		}
		wait = 24 * time.Hour
	}
}

// sweepImages удаляет из кэша картинки, которые каталогу больше не нужны; картинки других модулей
// (Options.KeepImages) остаются.
func (c *Catalog) sweepImages(ctx context.Context) error {
	if c.images == nil {
		return nil
	}
	keep, err := c.st.imageKeysInUse(ctx, c.now().Add(-imagesKeepFor))
	if err != nil {
		return dbError{err}
	}
	if c.keepImages != nil {
		extra, err := c.keepImages(ctx)
		if err != nil {
			return dbError{err}
		}
		for k := range extra {
			keep[k] = true
		}
	}
	removed, err := c.images.Sweep(func(k string) bool { return keep[k] }, c.now())
	if err != nil {
		c.log.Warn("каталог: кэш картинок не почистился", "err", err)
	}
	if err := c.st.forgetImages(ctx, removed); err != nil {
		return dbError{err}
	}
	if len(removed) > 0 {
		c.log.Info("каталог: из кэша удалены ненужные картинки", "count", len(removed))
	}
	return nil
}
