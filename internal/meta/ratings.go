package meta

import (
	"context"
	"errors"
	"log/slog"
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

// errNeedKey — задаче нужен поиск, а искать сейчас нечем (нет ключа или квоты).
var errNeedKey = errors.New("Кинопоиск: для поиска нужен ключ и квота")

// ProblemKinopoiskKey — проблема «ключ не подходит» в «Состоянии».
const ProblemKinopoiskKey = "kinopoisk.key"

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
}

// RatingsStatus — для страницы «Состояние» (этап 7).
type RatingsStatus struct {
	HasKey      bool
	BadKey      bool
	Quota       Quota     // последние известные лимиты ключа
	PausedUntil time.Time // квота кончилась — до этого времени только рейтинги по номеру без ключа
	Queue       int
}

type RatingsOptions struct {
	KP  *Kinopoisk
	DB  *store.DB
	Log *slog.Logger // nil — без журнала
}

// Ratings — модуль «ratings»: очередь запросов к Кинопоиску. Каталог ставит раздачи в очередь
// в порядке основного каталога (Enqueue) и берёт готовые рейтинги (For).
type Ratings struct {
	kp   *Kinopoisk
	st   ratingStore
	db   *store.DB
	log  *slog.Logger
	now  func() time.Time
	wake chan struct{}

	mu          sync.Mutex
	quota       Quota
	pausedUntil time.Time
	badKey      bool
	fails       int // ответов 5xx подряд (каждый платный)
}

func NewRatings(o RatingsOptions) *Ratings {
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	return &Ratings{kp: o.KP, st: ratingStore{o.DB}, db: o.DB, log: o.Log, now: time.Now, wake: make(chan struct{}, 1)}
}

func (r *Ratings) Name() string { return "ratings" }

// Enqueue ставит раздачу в очередь с приоритетом prio (меньше — раньше; каталог передаёт место в
// основном каталоге). Уже известное и свежее в очередь не попадает: каталог ставит всё при
// каждом обновлении, а квота не должна тратиться повторно.
func (r *Ratings) Enqueue(ctx context.Context, prio int, it Item) error {
	now := r.now()
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

// For — рейтинги раздач, для которых найден фильм.
func (r *Ratings) For(ctx context.Context, releases []string) (map[string]Rating, error) {
	return r.st.ratings(ctx, releases)
}

func (r *Ratings) Status(ctx context.Context) (RatingsStatus, error) {
	n, err := r.st.queueLen(ctx)
	r.mu.Lock()
	defer r.mu.Unlock()
	return RatingsStatus{HasKey: r.kp.HasKey(), BadKey: r.badKey, Quota: r.quota, PausedUntil: r.pausedUntil, Queue: n}, err
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
		case <-time.After(idlePoll):
		}
	}
}

// Step делает одну задачу очереди; did = false — делать сейчас нечего. Ошибки Кинопоиска
// задачу откладывают, а не роняют модуль; ошибка — только у базы.
func (r *Ratings) Step(ctx context.Context) (did bool, err error) {
	now := r.now()
	keyless := r.keyless(now)
	it, ok, err := r.st.next(ctx, now, keyless)
	if err != nil || !ok {
		return false, err
	}
	err = r.resolve(ctx, it, keyless)
	switch {
	case err == nil:
		return true, r.st.done(ctx, it)
	case ctx.Err() != nil:
		return false, nil
	case errors.Is(err, ErrQuota):
		r.log.Info("Кинопоиск: суточная квота исчерпана — до восстановления только рейтинги по номеру без ключа")
		r.mu.Lock()
		r.pausedUntil = now.Add(quotaRecheck)
		r.mu.Unlock()
		return true, nil
	case errors.Is(err, ErrBadKey):
		r.setBadKey(ctx)
		return true, nil
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

// keyless — сейчас только пути без ключа: ключа нет, он не подходит или кончилась квота.
func (r *Ratings) keyless(now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return !r.kp.HasKey() || r.badKey || now.Before(r.pausedUntil)
}

// resolve — фильм раздачи и его рейтинг. Номер из описания — без поиска; иначе IMDb, иначе
// поиск по названию (спека, раздел 8; хвост этапа 3 — связка через IMDb).
func (r *Ratings) resolve(ctx context.Context, it queued, keyless bool) error {
	now := r.now()
	kpID := it.KinopoiskID
	if kpID == 0 {
		id, _, _, err := r.st.releaseLink(ctx, it.Release)
		if err != nil {
			return err
		}
		kpID = id
	}
	if kpID == 0 {
		if keyless {
			return errNeedKey // next без ключа отдаёт только задачи с номером; на всякий случай — отложить, не терять
		}
		id, retryAt, err := r.findFilm(ctx, it)
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
	return r.st.saveFilm(ctx, f, now)
}

// findFilm ищет фильм раздачи без номера Кинопоиска: по IMDb (точно, один запрос), иначе по
// названию и году — сначала оригинальное (латиница), потом русское. Возвращает номер (0 — не
// найдено) и когда искать снова. Найденные фильмы сохраняются вместе с рейтингом из ответа.
func (r *Ratings) findFilm(ctx context.Context, it queued) (int, time.Time, error) {
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
	t := ParseTitle(it.Title)
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
	key := NormTitle(keywords[0])
	if id, retryAt, found, err := r.st.titleLink(ctx, key, t.Year); err != nil || (found && (id != 0 || now.Before(retryAt))) {
		return id, retryAt, err
	}
	broken := false
	for _, kw := range keywords {
		fs, err := r.kp.Search(ctx, kw, t.Year)
		var se *ServiceError
		if errors.As(err, &se) {
			broken = true // бывает на кириллице (исследование, разделы 12–13)
			r.serviceTrouble(now)
			continue
		}
		if err != nil {
			return 0, time.Time{}, err
		}
		r.serviceOK()
		if f, ok := match(fs, t); ok {
			if err := r.keepFilm(ctx, f, now); err != nil {
				return 0, time.Time{}, err
			}
			return f.ID, time.Time{}, r.st.setTitle(ctx, key, t.Year, f.ID, time.Time{})
		}
	}
	retry := now.Add(notFoundRetry)
	if broken {
		retry = now.Add(brokenSearchRetry)
	}
	return 0, retry, r.st.setTitle(ctx, key, t.Year, 0, retry)
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

// setBadKey — ключ не подходит: проблема в «Состоянии», дальше только пути без ключа.
func (r *Ratings) setBadKey(ctx context.Context) {
	r.mu.Lock()
	r.badKey = true
	r.mu.Unlock()
	if err := r.db.SetProblem(ctx, ProblemKinopoiskKey, ErrBadKey.Error()); err != nil {
		r.log.Error("не удалось записать проблему", "id", ProblemKinopoiskKey, "err", err)
	}
}
