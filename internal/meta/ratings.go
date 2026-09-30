package meta

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"time"

	"kinodom/internal/store"
	"kinodom/internal/supervisor"
)

// Сроки очереди рейтингов (спека, раздел 8; исследование, раздел 12).
const (
	ratingMaxAge      = 30 * 24 * time.Hour // рейтинг найденного фильма обновляется раз в 30 дней
	notFoundRetry     = 30 * 24 * time.Hour // не найдено — повтор через 30 дней
	brokenSearchRetry = 7 * 24 * time.Hour  // поиск ответил 5xx (бывает на кириллице) — повтор через неделю: каждая попытка тратит квоту
	serviceFailLimit  = 3                   // ответов 5xx подряд — и платный путь встаёт на час
	rateLimitRetry    = 2 * time.Second     // 429 — короткая пауза
	quotaRecheck      = time.Hour           // квота кончилась — проверять раз в час, пока не восстановится
	idlePoll          = 30 * time.Second    // очередь пуста или всё отложено — заглядывать снова
)

// errNeedKey — задаче нужен поиск, а искать сейчас нечем (нет ключа или квоты, без токена недоступно).
var errNeedKey = errors.New("Кинопоиск: для поиска нужен ключ и квота")

// errWebPaused — поиск без токена на паузе (отказ сайта, суточный предел), а ключа нет: задача ждёт.
var errWebPaused = errors.New("Кинопоиск без токена на паузе")

// ProblemKinopoiskKey — проблема «ключ не подходит» в «Состоянии».
const ProblemKinopoiskKey = "kinopoisk.key"

// ProblemKinopoiskBlocked — Кинопоиск без токена отказал: пауза (спека 11b, 5.2).
const ProblemKinopoiskBlocked = "kinopoisk.blocked"

// Item — раздача для очереди рейтингов. Ключ раздачи задаёт каталог: «rutor:1077013».
type Item struct {
	Release     string
	KinopoiskID int    // номер из ссылки в описании; 0 — ссылки нет
	IMDbID      string // «tt0133093»; "" — ссылки нет
	Title       string // название раздачи как есть
}

// Rating — фильм и рейтинг раздачи.
type Rating struct {
	KinopoiskID int
	Kinopoisk   float64 // 0 — рейтинга нет
	IMDb        float64
	NameRu      string
	NameOrig    string
	Year        int
	Type        string // FILM, TV_SERIES…; заполняет только Films (медиатека: вид карточки — Х11)
}

// RatingsStatus — для страницы «Состояние» (этап 7).
type RatingsStatus struct {
	HasKey      bool
	BadKey      bool
	Quota       Quota     // последние известные лимиты ключа
	PausedUntil time.Time // квота кончилась — до этого времени только рейтинги по номеру без ключа
	Queue       int
	Keyless     KPWebStatus // Кинопоиск без токена: пауза и запросов за сутки
}

type RatingsOptions struct {
	KP  *Kinopoisk
	Web *KPWeb // Кинопоиск без токена (спека 11b, раздел 5); nil — только ключ и номера из описаний
	DB  *store.DB
	Log *slog.Logger // nil — без журнала
}

// Ratings — модуль «ratings»: очередь запросов к Кинопоиску. Каталог ставит раздачи в очередь
// в порядке основного каталога (Enqueue) и берёт готовые рейтинги (For).
type Ratings struct {
	kp      *Kinopoisk
	web     *KPWeb
	st      ratingStore
	db      *store.DB
	log     *slog.Logger
	now     func() time.Time
	wake    chan struct{}
	recheck chan struct{} // ключ сменили: узнать лимиты заново (в цикле Run, не в запросе пульта)

	mu          sync.Mutex
	quota       Quota
	pausedUntil time.Time
	badKey      bool
	fails       int            // ответов 5xx подряд (каждый платный)
	webUntil    time.Time      // суточный предел без токена — до полуночи
	blocked     bool           // проблема kinopoisk.blocked записана
	kwFrom      map[string]int // раздача → с какого ключевого слова продолжить поиск по ключу (после 402/429)
}

func NewRatings(o RatingsOptions) *Ratings {
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	return &Ratings{kp: o.KP, web: o.Web, st: ratingStore{o.DB}, db: o.DB, log: o.Log, now: time.Now, wake: make(chan struct{}, 1),
		recheck: make(chan struct{}, 1), kwFrom: map[string]int{}}
}

func (r *Ratings) Name() string { return "ratings" }

// Enqueue ставит раздачу в очередь с приоритетом prio (меньше — раньше; каталог передаёт место в
// основном каталоге). Уже известное и свежее в очередь не попадает: каталог ставит всё при
// каждом обновлении, а квота не должна тратиться повторно.
func (r *Ratings) Enqueue(ctx context.Context, prio int, it Item) error {
	now := r.now()
	if it.KinopoiskID != 0 {
		if err := r.seedTitle(ctx, it); err != nil {
			return err
		}
	}
	id, retryAt, found, err := r.st.releaseLink(ctx, it.Release)
	if err != nil {
		return err
	}
	// Номер Кинопоиска из описания главнее найденного поиском, а новый номер после «не найдено» —
	// повод искать снова: описание раздачи из поиска приходит позже, при открытии карточки
	// (спека, раздел 7).
	newInfo := (it.KinopoiskID != 0 && it.KinopoiskID != id) || (id == 0 && it.IMDbID != "")
	if found && !newInfo {
		if id == 0 && now.Before(retryAt) {
			return nil // недавно не нашли
		}
		if id != 0 {
			at, ok, err := r.st.ratingAt(ctx, id)
			if err != nil {
				return err
			}
			if ok && now.Sub(at) < ratingMaxAge {
				return nil // рейтинг свежий
			}
		}
	}
	if err := r.st.enqueue(ctx, prio, it); err != nil {
		return err
	}
	select {
	case r.wake <- struct{}{}:
	default:
	}
	return nil
}

// seedTitle — соседняя раздача (спека 11b, 5.1, шаг 2): номер из описания раздачи сразу даёт номер
// её произведению в кэше названий — раздачи того же произведения без номера получат его без запросов.
// Найденный номер не перезаписывается; «не найдено» — перезаписывается.
func (r *Ratings) seedTitle(ctx context.Context, it Item) error {
	t := ParseTitle(it.Title)
	wk := WorkKey(t)
	if wk == "" {
		return nil
	}
	id, _, found, err := r.st.titleLink(ctx, wk, t.Year)
	if err != nil || (found && id != 0) {
		return err
	}
	return r.st.setTitle(ctx, wk, t.Year, it.KinopoiskID, time.Time{})
}

// EnqueueCatalog ставит раздачи основного каталога в его порядке: место в списке — приоритет.
// Стоявшие в очереди, но выпавшие из каталога, уходят в конец очереди (не удаляются: их мог
// поставить и поиск), — квота в дни первичного наполнения тратится на нынешний топ (ревью 5b).
func (r *Ratings) EnqueueCatalog(ctx context.Context, items []Item) error {
	if err := r.st.demoteAll(ctx); err != nil {
		return err
	}
	for i, it := range items {
		if err := r.Enqueue(ctx, i, it); err != nil {
			return err
		}
	}
	return nil
}

// For — рейтинги раздач, для которых найден фильм.
func (r *Ratings) For(ctx context.Context, releases []string) (map[string]Rating, error) {
	return r.st.ratings(ctx, releases)
}

// Releases — раздачи («rutor:1077013»), для которых найден каждый из фильмов kpIDs: раздачи одного
// фильма для карточки и «Других раздач» (спека этапа 7, раздел 10.4).
func (r *Ratings) Releases(ctx context.Context, kpIDs []int) (map[int][]string, error) {
	return r.st.releasesOf(ctx, kpIDs)
}

// Films — рейтинги фильмов по номерам (медиатека); кого нет в базе — нет и в ответе.
func (r *Ratings) Films(ctx context.Context, ids []int) (map[int]Rating, error) {
	return r.st.films(ctx, ids)
}

// AddFilm — фильм, найденный медиатекой, в базу рейтингов; нулевой рейтинг не затирает известный.
func (r *Ratings) AddFilm(ctx context.Context, f Film) error { return r.st.addFilm(ctx, f, r.now()) }

func (r *Ratings) Status(ctx context.Context) (RatingsStatus, error) {
	n, err := r.st.queueLen(ctx)
	var kw KPWebStatus
	if r.web != nil {
		kw = r.web.Status()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return RatingsStatus{HasKey: r.kp.HasKey(), BadKey: r.badKey, Quota: r.quota, PausedUntil: r.pausedUntil, Queue: n, Keyless: kw}, err
}

// Run — первый запрос с ключом — лимиты (спека, раздел 8), затем очередь по одной задаче.
func (r *Ratings) Run(ctx context.Context) error {
	r.checkQuota(ctx)
	supervisor.Ready(ctx)
	hourly := time.NewTicker(quotaRecheck)
	defer hourly.Stop()
	for {
		did, err := r.Step(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			return err // ошибка базы — сбой модуля, сторож перезапустит
		}
		if did {
			continue // темп задаёт ограничитель клиента (3 запроса/с)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-r.wake:
		case <-hourly.C:
			r.checkQuota(ctx)
		case <-r.recheck:
			r.checkQuota(ctx)
		case <-time.After(idlePoll):
		}
	}
}

// Step делает одну задачу очереди; did = false — делать сейчас нечего. Ошибки Кинопоиска
// задачу откладывают, а не роняют модуль; ошибка — только у базы.
func (r *Ratings) Step(ctx context.Context) (did bool, err error) {
	now := r.now()
	web, key := r.ways(now)
	it, ok, err := r.st.next(ctx, now, !web && !key)
	if err != nil || !ok {
		return false, err
	}
	err = r.resolve(ctx, it, web, key)
	keyless := !key
	switch {
	case err == nil:
		return true, r.st.done(ctx, it)
	case ctx.Err() != nil:
		return false, nil
	case errors.Is(err, ErrQuota):
		r.log.Info("Кинопоиск: суточная квота ключа исчерпана — до восстановления без ключа")
		r.mu.Lock()
		r.pausedUntil = now.Add(quotaRecheck)
		r.mu.Unlock()
		return true, r.waitKey(ctx, it, web, now)
	case errors.Is(err, ErrBadKey):
		r.setBadKey(ctx)
		return true, r.waitKey(ctx, it, web, now)
	case errors.Is(err, errWebPaused):
		return true, r.st.postpone(ctx, it.Release, r.webResume(now))
	case errors.Is(err, errNeedKey):
		return true, r.st.postpone(ctx, it.Release, now.Add(quotaRecheck))
	case errors.Is(err, ErrRateLimited):
		return true, r.st.postpone(ctx, it.Release, now.Add(rateLimitRetry))
	default:
		var se *ServiceError
		if errors.As(err, &se) && !keyless {
			r.serviceTrouble(now)
		}
		delay := retryDelay(it.Attempts + 1)
		r.log.Warn("Кинопоиск: не вышло, повтор позже", "release", it.Release, "delay", delay, "err", err)
		return true, r.st.failed(ctx, it.Release, now.Add(delay))
	}
}

// retryDelay — пауза после n-й неудачи подряд: 5 минут, час, сутки, дальше неделя. Сбой, который
// повторяется (битая запись у Кинопоиска), иначе тратил бы квоту каждые 5 минут: ответ 5xx тоже
// платный (исследование, раздел 12; ревью этапа 5b — 288 запросов в сутки на одну раздачу).
func retryDelay(n int) time.Duration {
	switch {
	case n <= 1:
		return 5 * time.Minute
	case n == 2:
		return time.Hour
	case n == 3:
		return 24 * time.Hour
	}
	return 7 * 24 * time.Hour
}

// serviceTrouble — Кинопоиск ответил 5xx. Три таких ответа подряд — сбой сервиса: платный путь
// встаёт на час (рейтинги по номеру без ключа идут дальше), иначе очередь за минуты прошла бы по
// сотне раздач и потратила суточную квоту.
func (r *Ratings) serviceTrouble(now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.fails++
	if r.fails >= serviceFailLimit {
		r.fails = 0
		if until := now.Add(quotaRecheck); until.After(r.pausedUntil) {
			r.pausedUntil = until
		}
		r.log.Warn("Кинопоиск: сбой сервиса — платные запросы на час остановлены")
	}
}

// serviceOK — платный запрос прошёл: счёт ответов 5xx подряд — заново.
func (r *Ratings) serviceOK() {
	r.mu.Lock()
	r.fails = 0
	r.mu.Unlock()
}

// waitKey — ключ отказал (квота, не подходит): задача, которой без ключа дальше некуда (номера нет, без
// токена недоступно), ждёт проверки квоты, а не берётся снова (Х1); с номером или без токена — идёт сразу.
func (r *Ratings) waitKey(ctx context.Context, it queued, web bool, now time.Time) error {
	if web || it.KinopoiskID != 0 {
		return nil
	}
	id, _, _, err := r.st.releaseLink(ctx, it.Release)
	if err != nil || id != 0 {
		return err
	}
	return r.st.postpone(ctx, it.Release, now.Add(quotaRecheck))
}

// ways — чем сейчас можно искать номер: без токена (сайт не на паузе) и ключом (задан, подходит, есть квота).
func (r *Ratings) ways(now time.Time) (web, key bool) {
	r.mu.Lock()
	key = r.kp.HasKey() && !r.badKey && !now.Before(r.pausedUntil)
	webUntil := r.webUntil
	r.mu.Unlock()
	if r.web != nil && !now.Before(webUntil) {
		web = !now.Before(r.web.Status().PausedUntil)
	}
	return web, key
}

// webResume — когда без токена можно снова: конец паузы сайта или суточного предела.
func (r *Ratings) webResume(now time.Time) time.Time {
	r.mu.Lock()
	until := r.webUntil
	r.mu.Unlock()
	if r.web != nil {
		if p := r.web.Status().PausedUntil; p.After(until) {
			until = p
		}
	}
	if !until.After(now) {
		until = now.Add(quotaRecheck)
	}
	return until
}

// webPaused — без токена отказ или предел: проблема в «Состоянии» (отказ) или пауза до полуночи (предел).
func (r *Ratings) webPaused(ctx context.Context, err error) {
	if errors.Is(err, ErrKPDailyLimit) {
		now := r.now()
		y, m, d := now.Date()
		r.mu.Lock()
		r.webUntil = time.Date(y, m, d+1, 0, 0, 0, 0, now.Location())
		r.mu.Unlock()
		return
	}
	st := r.web.Status()
	text := "Кинопоиск не отвечает — " + st.Reason
	if !st.PausedUntil.IsZero() {
		text += ", пауза до " + st.PausedUntil.Format("15:04")
	}
	r.mu.Lock()
	r.blocked = true
	r.mu.Unlock()
	if err := r.db.SetProblem(ctx, ProblemKinopoiskBlocked, text); err != nil {
		r.log.Error("не удалось записать проблему", "id", ProblemKinopoiskBlocked, "err", err)
	}
}

// webOK — запрос без токена прошёл: проблема отказа снимается.
func (r *Ratings) webOK(ctx context.Context) {
	r.mu.Lock()
	was := r.blocked
	r.blocked = false
	r.mu.Unlock()
	if was {
		if err := r.db.ClearProblem(ctx, ProblemKinopoiskBlocked); err != nil {
			r.log.Error("не удалось снять проблему", "id", ProblemKinopoiskBlocked, "err", err)
		}
	}
}

// resolve — фильм раздачи и его рейтинг. Номер из описания — без поиска; иначе соседняя раздача,
// поиск без токена, ключ (спека 11b, раздел 5.1).
func (r *Ratings) resolve(ctx context.Context, it queued, web, key bool) error {
	now := r.now()
	keyless := !key
	kpID := it.KinopoiskID
	if kpID == 0 {
		id, _, _, err := r.st.releaseLink(ctx, it.Release)
		if err != nil {
			return err
		}
		kpID = id
	}
	if kpID == 0 {
		id, retryAt, err := r.findFilm(ctx, it, web, key)
		if err != nil {
			return err
		}
		if err := r.st.link(ctx, it.Release, id, retryAt); err != nil || id == 0 {
			return err
		}
		kpID = id
	} else if err := r.st.link(ctx, it.Release, kpID, time.Time{}); err != nil {
		return err
	}
	at, ok, err := r.st.ratingAt(ctx, kpID)
	if err != nil || (ok && now.Sub(at) < ratingMaxAge) {
		return err
	}
	if keyless {
		kp, imdb, err := r.kp.KeylessRating(ctx, kpID)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		return r.st.setRating(ctx, kpID, kp, imdb, now) // нет в rating.kinopoisk.ru — рейтинга нет
	}
	f, err := r.kp.Film(ctx, kpID)
	if err == nil || errors.Is(err, ErrNotFound) {
		r.serviceOK()
	}
	if errors.Is(err, ErrNotFound) {
		return r.st.link(ctx, it.Release, 0, now.Add(notFoundRetry)) // номер из описания устарел
	}
	if err != nil {
		return err
	}
	// Фильм нашёлся поиском по названию записью без года, а в карточке год есть и чужой — это не
	// тот фильм: без рейтинга лучше, чем с чужим (ревью 5b, M1).
	if t := ParseTitle(it.Title); it.KinopoiskID == 0 && it.IMDbID == "" && t.Year != 0 && f.Year != 0 && abs(f.Year-t.Year) > 1 {
		if err := r.st.link(ctx, it.Release, 0, now.Add(notFoundRetry)); err != nil {
			return err
		}
		return r.st.setTitle(ctx, titleKey(t), t.Year, 0, now.Add(notFoundRetry))
	}
	return r.st.saveFilm(ctx, f, now)
}

// titleKey — ключ кэша «название и год → фильм»: оригинальное название, если есть, иначе русское.
func titleKey(t Title) string {
	if t.Orig != "" {
		return NormTitle(t.Orig)
	}
	return NormTitle(t.Ru)
}

// findFilm ищет фильм раздачи без номера Кинопоиска (спека 11b, 5.1): кэш названий (там и соседние
// раздачи), поиск сайта без токена, а когда сайт на паузе или не нашёл — ключ. Возвращает номер
// (0 — не найдено) и когда искать снова.
func (r *Ratings) findFilm(ctx context.Context, it queued, web, key bool) (int, time.Time, error) {
	now := r.now()
	t := ParseTitle(it.Title)
	wk := WorkKey(t)
	miss := time.Time{} // сайт не нашёл: искать снова не раньше
	if wk != "" {
		id, retryAt, found, err := r.st.titleLink(ctx, wk, t.Year)
		if err != nil {
			return 0, time.Time{}, err
		}
		if found && id != 0 {
			return id, time.Time{}, nil
		}
		if found && now.Before(retryAt) {
			miss = retryAt
		}
	}
	if web && wk != "" && miss.IsZero() {
		id, err := r.searchWeb(ctx, t, wk)
		switch {
		case err == nil && id != 0:
			return id, time.Time{}, nil
		case err == nil:
			miss = now.Add(notFoundRetry)
		case errors.Is(err, ErrKPBlocked) || errors.Is(err, ErrKPDailyLimit):
			r.webPaused(ctx, err)
			if !key {
				return 0, time.Time{}, errWebPaused
			}
		default:
			return 0, time.Time{}, err
		}
	}
	if key {
		return r.findByKey(ctx, it, t)
	}
	if !miss.IsZero() {
		return 0, miss, nil
	}
	if r.web != nil {
		return 0, time.Time{}, errWebPaused
	}
	return 0, time.Time{}, errNeedKey
}

// searchWeb — поиск сайта без токена: русское название, потом оригинальное; год не в запросе, сверка —
// MatchKP. Найденный фильм — в базу с рейтингом из ответа (без второго запроса), итог — в кэш названий.
func (r *Ratings) searchWeb(ctx context.Context, t Title, wk string) (int, error) {
	now := r.now()
	var keywords []string
	for _, n := range []string{t.Ru, t.Orig} {
		if n != "" && !slices.ContainsFunc(keywords, func(k string) bool { return NormTitle(k) == NormTitle(n) }) {
			keywords = append(keywords, n)
		}
	}
	for _, kw := range keywords {
		hits, err := r.web.Suggest(ctx, KPNormal, kw)
		if err != nil {
			return 0, err
		}
		r.webOK(ctx)
		if f, ok := MatchKP(hits, t); ok {
			if err := r.st.addFilm(ctx, f, now); err != nil {
				return 0, err
			}
			return f.ID, r.st.setTitle(ctx, wk, t.Year, f.ID, time.Time{})
		}
	}
	return 0, r.st.setTitle(ctx, wk, t.Year, 0, now.Add(notFoundRetry))
}

// findByKey — поиск ключом (kinopoiskapiunofficial.tech): по IMDb (точно, один запрос), иначе по
// названию и году — сначала оригинальное (латиница), потом русское. Повтор после 402/429 продолжает с
// того ключевого слова, на котором остановился (Х1).
func (r *Ratings) findByKey(ctx context.Context, it queued, t Title) (int, time.Time, error) {
	now := r.now()
	if it.IMDbID != "" {
		// Рипы одного фильма со ссылкой на IMDb — один запрос: сначала то, что уже известно.
		id, retryAt, found, err := r.st.imdbLink(ctx, it.IMDbID)
		if err != nil {
			return 0, time.Time{}, err
		}
		switch {
		case found && id != 0:
			return id, time.Time{}, nil
		case found && now.Before(retryAt):
			// Кинопоиск этот IMDb не знает — сразу к поиску по названию.
		default:
			f, err := r.kp.ByIMDb(ctx, it.IMDbID)
			switch {
			case err == nil:
				r.serviceOK()
				if err := r.st.setIMDb(ctx, it.IMDbID, f.ID, time.Time{}); err != nil {
					return 0, time.Time{}, err
				}
				return f.ID, time.Time{}, r.keepFilm(ctx, f, now)
			case errors.Is(err, ErrNotFound):
				r.serviceOK()
				if err := r.st.setIMDb(ctx, it.IMDbID, 0, now.Add(notFoundRetry)); err != nil {
					return 0, time.Time{}, err
				}
			default:
				return 0, time.Time{}, err
			}
		}
	}
	keywords := []string{}
	if t.Orig != "" {
		keywords = append(keywords, t.Orig)
	}
	if t.Ru != "" && t.Ru != t.Orig {
		keywords = append(keywords, t.Ru)
	}
	if len(keywords) == 0 {
		return 0, now.Add(notFoundRetry), nil
	}
	key := titleKey(t)
	if id, retryAt, found, err := r.st.titleLink(ctx, key, t.Year); err != nil || (found && (id != 0 || now.Before(retryAt))) {
		return id, retryAt, err
	}
	broken := false
	r.mu.Lock()
	from := r.kwFrom[it.Release]
	r.mu.Unlock()
	for i := from; i < len(keywords); i++ {
		fs, err := r.kp.Search(ctx, keywords[i], t.Year)
		var se *ServiceError
		if errors.As(err, &se) {
			broken = true // бывает на кириллице (исследование, разделы 12–13)
			r.serviceTrouble(now)
			continue
		}
		if err != nil {
			r.mu.Lock()
			r.kwFrom[it.Release] = i
			r.mu.Unlock()
			return 0, time.Time{}, err
		}
		r.serviceOK()
		if f, ok := match(fs, t); ok {
			r.forgetKeyword(it.Release)
			if err := r.keepFilm(ctx, f, now); err != nil {
				return 0, time.Time{}, err
			}
			return f.ID, time.Time{}, r.st.setTitle(ctx, key, t.Year, f.ID, time.Time{})
		}
	}
	r.forgetKeyword(it.Release)
	retry := now.Add(notFoundRetry)
	if broken {
		retry = now.Add(brokenSearchRetry)
	}
	return 0, retry, r.st.setTitle(ctx, key, t.Year, 0, retry)
}

func (r *Ratings) forgetKeyword(release string) {
	r.mu.Lock()
	delete(r.kwFrom, release)
	r.mu.Unlock()
}

// keepFilm сохраняет фильм из выдачи поиска, только если в ней есть год и рейтинг. Выдача отстаёт
// от карточки фильма: у новинок там null (вживую — «Бегущая» 6549627). Тогда рейтинг возьмёт
// шаг «по номеру» в resolve — один запрос, а не 0 на 30 дней.
func (r *Ratings) keepFilm(ctx context.Context, f Film, now time.Time) error {
	if f.Year == 0 || f.Rating == 0 {
		return nil
	}
	return r.st.saveFilm(ctx, f, now)
}

// match — фильм, у которого русское или оригинальное название совпадает с одним из названий
// раздачи, а год — с точностью до года. Иначе не найдено: без рейтинга лучше, чем с чужим.
// У записей Кинопоиска бывает не указан год (заглушки и новинки): такая запись засчитывается,
// только если у раздачи есть год, совпали оба названия — русское и оригинальное — и записи с
// подходящим годом нет. Вживую «Бегущая / The Runner (2026)» иначе получила бы 589920
// «The Runner» без года вместо 6549627 (исследование, раздел 13).
func match(fs []Film, t Title) (Film, bool) {
	names := map[string]bool{}
	for _, n := range t.Names {
		names[NormTitle(n)] = true
	}
	var yearless *Film
	for i, f := range fs {
		ru := f.NameRu != "" && names[NormTitle(f.NameRu)]
		orig := f.NameOrig != "" && names[NormTitle(f.NameOrig)]
		switch {
		case !ru && !orig:
		case t.Year == 0:
			return f, true
		case f.Year != 0:
			if abs(f.Year-t.Year) <= 1 {
				return f, true
			}
		case ru && orig && yearless == nil:
			yearless = &fs[i]
		}
	}
	if yearless != nil {
		return *yearless, true
	}
	return Film{}, false
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// checkQuota — лимиты ключа: их видно в «Состоянии»; квота восстановилась — снять паузу.
func (r *Ratings) checkQuota(ctx context.Context) {
	if !r.kp.HasKey() {
		return
	}
	q, err := r.kp.Quota(ctx)
	switch {
	case errors.Is(err, ErrBadKey):
		r.setBadKey(ctx)
		return
	case err != nil:
		r.log.Warn("Кинопоиск: лимиты ключа не узнать", "err", err)
		return
	}
	now := r.now()
	r.mu.Lock()
	r.quota, r.badKey = q, false
	if q.DailyLimit > 0 && q.DailyUsed >= q.DailyLimit {
		r.pausedUntil = now.Add(quotaRecheck)
	} else {
		r.pausedUntil = time.Time{}
	}
	r.mu.Unlock()
	if err := r.db.ClearProblem(ctx, ProblemKinopoiskKey); err != nil {
		r.log.Error("не удалось снять проблему", "id", ProblemKinopoiskKey, "err", err)
	}
}

// KeyChanged — ключ сменили в настройках (Kinopoisk.SetKey уже вызван): прежние «ключ не подходит»
// и пауза квоты относились к старому ключу. Лимиты нового ключа модуль узнает в своём цикле —
// запрос пульта не ждёт Кинопоиск (хвост 5b: раньше ключ читался только при старте).
func (r *Ratings) KeyChanged(ctx context.Context) {
	r.mu.Lock()
	r.badKey, r.pausedUntil, r.quota, r.fails = false, time.Time{}, Quota{}, 0
	r.mu.Unlock()
	if err := r.db.ClearProblem(ctx, ProblemKinopoiskKey); err != nil {
		r.log.Error("не удалось снять проблему", "id", ProblemKinopoiskKey, "err", err)
	}
	for _, ch := range []chan struct{}{r.recheck, r.wake} {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// setBadKey — ключ не подходит: проблема в «Состоянии», дальше только пути без ключа.
func (r *Ratings) setBadKey(ctx context.Context) {
	r.mu.Lock()
	r.badKey = true
	r.mu.Unlock()
	if err := r.db.SetProblem(ctx, ProblemKinopoiskKey, ErrBadKey.Error()); err != nil {
		r.log.Error("не удалось записать проблему", "id", ProblemKinopoiskKey, "err", err)
	}
}
