package catalog

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strconv"
	"time"

	"kinodom/internal/meta"
)

// ErrNoRelease — такой раздачи в Kinodom нет.
var ErrNoRelease = errors.New("такой раздачи нет")

// Release — раздача для экрана раздачи.
type Release struct {
	Entry
	Description    string
	TrackerURL     string // страница раздачи на трекере через текущее зеркало; "" — неизвестна
	Magnet         string
	Torrent        []byte // .torrent Rutor, скачанный заранее; nil — нет
	DetailsPending bool   // страницу раздачи ещё не загружали: она поставлена в догрузку первой
}

// ReleaseView — раздача для API; detailsPending — в EntryView.
type ReleaseView struct {
	EntryView
	Description string `json:"description"`
	TrackerURL  string `json:"trackerUrl"`
	Hash        string `json:"hash"` // infohash; "" — ещё неизвестен
}

func (r Release) View() ReleaseView {
	ev := r.Entry.View()
	ev.DetailsPending = r.DetailsPending // снятую с трекера раздачу не догружают — и не ждут
	return ReleaseView{EntryView: ev, Description: r.Description, TrackerURL: r.TrackerURL, Hash: r.InfoHash}
}

// ReleaseRef — раздача, из которой скачан файл: экран «Загрузки» показывает её название и постер, а
// сезон и качество различают раздачи одного сериала (финальное ревью 7b).
type ReleaseRef struct {
	ID       int64  `json:"id"`
	Title    string `json:"title"`
	ImageKey string `json:"imageKey"`
	Season   string `json:"season"`  // «S01», «Сезон: 1, Серии: 1-8 из 10»; "" — нет
	Quality  string `json:"quality"` // «WEB-DL 1080p»; "" — не найдено
}

// ReleasesByHash — раздачи каталога по infohash (нижний регистр); одинаковый infohash у двух
// трекеров — та, где больше раздающих.
func (c *Catalog) ReleasesByHash(ctx context.Context, hashes []string) (map[string]ReleaseRef, error) {
	out := map[string]ReleaseRef{}
	for _, h := range hashes {
		if _, ok := out[h]; ok || h == "" {
			continue
		}
		var r ReleaseRef
		err := c.db.R.QueryRowContext(ctx,
			`SELECT id, title, image_key FROM releases WHERE infohash = ? AND removed = 0 ORDER BY seeders DESC LIMIT 1`, h).
			Scan(&r.ID, &r.Title, &r.ImageKey)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			continue
		case err != nil:
			return nil, err
		}
		t := meta.ParseTitle(r.Title)
		r.Season, r.Quality = t.Season, t.Quality
		out[h] = r
	}
	return out, nil
}

// magnetBuilder — источник собирает magnet из infohash со своими трекерами. У Rutracker без них
// старт только через DHT — 35 с вместо 6–13 (основная спека, раздел 6).
type magnetBuilder interface {
	Magnet(infohash string) string
}

// topicURLer — источник знает адрес страницы раздачи (ссылка «На трекере»).
type topicURLer interface {
	TopicURL(topicID string) string
}

// Release — раздача по номеру. Страницу раздачи ещё не загружали (найдено поиском или ещё не дошла
// очередь) — она догружается вне очереди, первой (хвост 5c): пульт повторяет запрос, пока
// DetailsPending. Нет картинки, а номер Кинопоиска известен — постер Кинопоиска догружается тоже.
func (c *Catalog) Release(ctx context.Context, id int64) (Release, error) {
	r, desc, magnet, torrent, removed, err := c.st.release(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return Release{}, ErrNoRelease
	}
	if err != nil {
		return Release{}, err
	}
	es, err := c.entries(ctx, []row{r})
	if err != nil {
		return Release{}, err
	}
	out := Release{Entry: es[0], Description: desc, Magnet: magnet, Torrent: torrent}
	if tu, ok := c.sources[r.Tracker].(topicURLer); ok {
		out.TrackerURL = tu.TopicURL(r.TopicID)
	}
	// Страницы раздачи ещё нет, а infohash известен из списка — «Скачать» не ждёт догрузки.
	if out.Magnet == "" && r.InfoHash != "" {
		out.Magnet = "magnet:?xt=urn:btih:" + r.InfoHash
		if mb, ok := c.sources[r.Tracker].(magnetBuilder); ok {
			out.Magnet = mb.Magnet(r.InfoHash)
		}
	}
	if r.DetailsAt.IsZero() && !removed {
		out.DetailsPending = true
		c.enrichSoon(r.Tracker, r.ID)
	}
	if !r.DetailsAt.IsZero() && !removed && out.Torrent == nil && c.torrentOnOpen(r) {
		out.DetailsPending = true
	}
	if out.ImageKey == "" && !r.DetailsAt.IsZero() {
		// Картинки нет, а человек открыл раздачу — постер (страницы или Кинопоиска) без паузы повтора.
		c.mu.Lock()
		c.forced[r.ID] = true
		c.mu.Unlock()
		c.wakePosters()
	}
	return out, nil
}

// enrichSoon ставит раздачу в догрузку первой: её страницу ждёт человек.
func (c *Catalog) enrichSoon(tracker string, id int64) {
	c.mu.Lock()
	if !slices.Contains(c.urgent[tracker], id) {
		c.urgent[tracker] = append(c.urgent[tracker], id)
	}
	c.interruptLocked(tracker)
	c.mu.Unlock()
	if ch, ok := c.enrichWake[tracker]; ok {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// foundLimit — просьб догрузки вне очереди на трекер: при быстрой прокрутке старые отбрасываются.
const foundLimit = 200

// findSoon ставит найденное поиском и показанную порцию каталога в догрузку вне очереди — после открытых
// в пульте, в начало: видимое сейчас — первым, по порядку показа; прежние просьбы — за ним (ревью 11b-Г).
func (c *Catalog) findSoon(tracker string, ids []int64) {
	c.mu.Lock()
	q := make([]int64, 0, len(ids)+len(c.found[tracker]))
	q = append(q, ids...)
	for _, id := range c.found[tracker] {
		if !slices.Contains(ids, id) {
			q = append(q, id)
		}
	}
	c.found[tracker] = q[:min(len(q), foundLimit)]
	c.interruptLocked(tracker)
	c.mu.Unlock()
	if ch, ok := c.enrichWake[tracker]; ok {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// yieldTo — фоновый шаг трекера ждёт .torrent: срочная работа (открыли раздачу, нашли поиском) его
// прерывает (d.rutor.info отдаёт .torrent 4–10 с, бывает и 20). Уже пришедшая — сразу. nil — шаг
// дождался.
func (c *Catalog) yieldTo(tracker string, interrupt func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if interrupt == nil {
		delete(c.yield, tracker)
		return
	}
	c.yield[tracker] = interrupt
	if len(c.urgent[tracker])+len(c.found[tracker]) > 0 {
		c.interruptLocked(tracker)
	}
}

func (c *Catalog) interruptLocked(tracker string) {
	if f := c.yield[tracker]; f != nil {
		f()
		delete(c.yield, tracker)
	}
}

// nextUrgent — раздача из догрузки вне очереди: сначала открытые в пульте (opened), потом найденные
// поиском; ok = false — таких нет.
func (c *Catalog) nextUrgent(ctx context.Context, tracker string) (r row, opened, ok bool, err error) {
	for {
		c.mu.Lock()
		q, isOpened := c.urgent, true
		if len(q[tracker]) == 0 {
			q, isOpened = c.found, false
		}
		ids := q[tracker]
		if len(ids) == 0 {
			c.mu.Unlock()
			return row{}, false, false, nil
		}
		id := ids[0]
		q[tracker] = ids[1:]
		c.mu.Unlock()
		rs, err := c.st.rowsByID(ctx, []int64{id})
		if err != nil {
			return row{}, false, false, err
		}
		if r, ok := rs[id]; ok && r.DetailsAt.IsZero() {
			return r, isOpened, true, nil
		}
	}
}

// torrentOnOpen — открыли раздачу, догруженную без .torrent (найдена поиском): .torrent качается
// сразу, экран раздачи ждёт его, как страницу (без него сериал Rutor показался бы без серий). true —
// качается; false — не нужен или недавно не скачался (раздача откроется по magnet, повтор в фоне).
func (c *Catalog) torrentOnOpen(r row) bool {
	tf, ok := c.sources[r.Tracker].(torrentFetcher)
	if !ok || !c.configured(r.Tracker) || !c.due("torrent", r.ID, c.now()) {
		return false
	}
	c.mu.Lock()
	if c.torrentNow[r.ID] {
		c.mu.Unlock()
		return true
	}
	c.torrentNow[r.ID] = true
	base := c.runCtx
	c.mu.Unlock()
	if base == nil {
		base = context.Background()
	}
	c.posterWG.Add(1)
	go func() {
		defer c.posterWG.Done()
		defer func() {
			c.mu.Lock()
			delete(c.torrentNow, r.ID)
			c.mu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(base, torrentWait)
		b, err := tf.Torrent(ctx, r.TopicID)
		cancel()
		if base.Err() != nil {
			return
		}
		if err != nil {
			c.log.Warn("каталог: .torrent открытой раздачи не скачался — откроется по magnet", "tracker", r.Tracker, "topic", r.TopicID, "err", err)
			c.failed("torrent", r.ID, c.now())
			return
		}
		c.succeeded("torrent", r.ID)
		if err := c.st.saveTorrent(context.WithoutCancel(base), r.ID, b); err != nil {
			c.log.Warn("каталог: .torrent не записался", "err", err)
		}
	}()
	return true
}

// Постеры и .torrent про запас (спека, раздел 8; хвосты 5c и Х7): номер фильма нашёлся очередью
// рейтингов позже, чем догрузилась страница раздачи; хостинг картинки или .torrent Rutor не ответили.
const (
	postersEvery  = 10 * time.Minute
	assetsPerPass = 20 // повторов за проход: мёртвый хостинг держит до минуты
)

// assetRetry — паузы повторов после сбоя: 10 мин, 1 ч, 6 ч, дальше — раз в сутки. В памяти: после
// перезапуска — снова с 10 минут.
var assetRetry = []time.Duration{10 * time.Minute, time.Hour, 6 * time.Hour, 24 * time.Hour}

type retryState struct {
	n    int
	next time.Time
}

// failed — постер или .torrent не скачался: следующая попытка — после паузы.
func (c *Catalog) failed(kind string, id int64, now time.Time) {
	k := kind + ":" + strconv.FormatInt(id, 10)
	c.mu.Lock()
	defer c.mu.Unlock()
	st := c.retries[k]
	st.n++
	st.next = now.Add(assetRetry[min(st.n, len(assetRetry))-1])
	c.retries[k] = st
}

// due — пора пробовать: ещё не пробовали, прошла пауза или (постер) раздачу открыли.
func (c *Catalog) due(kind string, id int64, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if kind == "poster" && c.forced[id] {
		delete(c.forced, id)
		return true
	}
	st, ok := c.retries[kind+":"+strconv.FormatInt(id, 10)]
	return !ok || !now.Before(st.next)
}

func (c *Catalog) succeeded(kind string, id int64) {
	c.mu.Lock()
	delete(c.retries, kind+":"+strconv.FormatInt(id, 10))
	c.mu.Unlock()
}

// bgPostersQueued — сколько фоновых постеров может ждать места.
const bgPostersQueued = 4

// posterLater — постер раздачи вне шага догрузки (хвост Х8): срочная раздача не ждёт чужой медленный
// хостинг. urgent (открытая, найденная) — свои два места, не за фоновыми (ревью 11b-А); фоновых — два
// места и не больше bgPostersQueued ждущих, остальные подберёт fixPosters.
func (c *Catalog) posterLater(ctx context.Context, id int64, url string, kp int, urgent bool) {
	if c.images == nil || (url == "" && (kp == 0 || c.kpPoster == nil)) {
		return
	}
	sem := c.urgentSem
	if !urgent {
		c.mu.Lock()
		if c.bgWaiting >= bgPostersQueued {
			c.mu.Unlock()
			return // очередь фоновых полна: постер подберёт fixPosters
		}
		c.bgWaiting++
		c.mu.Unlock()
		sem = c.posterSem
	}
	waited := func() {
		if !urgent {
			c.mu.Lock()
			c.bgWaiting--
			c.mu.Unlock()
		}
	}
	c.posterWG.Add(1)
	go func() {
		defer c.posterWG.Done()
		select {
		case sem <- struct{}{}:
			waited()
		case <-ctx.Done():
			waited()
			return
		}
		defer func() { <-sem }()
		key := c.fetchPoster(ctx, url, kp)
		if ctx.Err() != nil {
			return
		}
		if key == "" {
			c.failed("poster", id, c.now())
			return
		}
		c.succeeded("poster", id)
		c.noteKPPoster(id, kp, key)
		if err := c.st.saveImageKey(context.WithoutCancel(ctx), id, key); err != nil {
			c.log.Warn("каталог: постер не записался", "err", err)
		}
	}()
}

// noteKPPoster — номер известен, а картинка не Кинопоиска (он не отдал постер): повтор постера
// Кинопоиска — по паузе (Х9), ключ повтора «kpposter».
func (c *Catalog) noteKPPoster(id int64, kp int, key string) {
	if kp > 0 && c.kpPoster != nil && key != meta.ImageKey(c.kpPoster(kp)) {
		c.failed("kpposter", id, c.now())
	}
}

// kpSwitchPerPass — сколько постеров страниц за проход меняется на постер Кинопоиска: он прямой и
// быстрый, а у каталога, где номера нашлись позже, таких сотни.
const kpSwitchPerPass = 60

func (c *Catalog) wakePosters() {
	select {
	case c.postersWake <- struct{}{}:
	default:
	}
}

// postersLoop раз в 10 минут (и сразу, когда открыли раздачу без картинки) догружает постеры
// Кинопоиска раздачам без картинки.
func (c *Catalog) postersLoop(ctx context.Context) error {
	for {
		if err := c.fixPosters(ctx); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-c.postersWake:
		case <-time.After(postersEvery):
		}
	}
}

// switchToKPPosters — раздачам с картинкой, у которых номер Кинопоиска известен, а картинка не его, —
// постер Кинопоиска (не больше kpSwitchPerPass за проход).
func (c *Catalog) switchToKPPosters(ctx context.Context, now time.Time) error {
	if c.kpPoster == nil {
		return nil
	}
	rs, err := c.st.postersWithImage(ctx, now.Add(-imagesKeepFor))
	if err != nil || len(rs) == 0 {
		return err
	}
	kps, err := c.kinopoiskIDs(ctx, rs)
	if err != nil {
		return err
	}
	n := 0
	for _, r := range rs {
		kp := kps[r.ID]
		if kp == 0 || n == kpSwitchPerPass {
			continue
		}
		u := c.kpPoster(kp)
		if r.ImageKey == meta.ImageKey(u) || !c.due("kpposter", r.ID, now) {
			continue
		}
		n++
		key, err := c.images.Fetch(ctx, u, meta.Direct)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			c.failed("kpposter", r.ID, now)
			continue
		}
		c.succeeded("kpposter", r.ID)
		if err := c.st.saveImageKey(ctx, r.ID, key); err != nil {
			return err
		}
	}
	return nil
}

// fixPosters — повтор постеров и .torrent Rutor (хвост Х7): постер Кинопоиска по номеру (из описания или
// найденному очередью рейтингов), а если номера нет или Кинопоиск не отдал — постер страницы;
// .torrent — у источников, которые его отдают. Сначала открытые в пульте, за проход — не больше
// assetsPerPass. Ошибка — только у базы.
func (c *Catalog) fixPosters(ctx context.Context) error {
	now := c.now()
	budget := assetsPerPass
	if c.images != nil {
		// Картинки, признанные заглушками хостинга (хвост Х6), снимаются с раздач — ниже им постер
		// страницы (заглушка — «не картинка») или Кинопоиска.
		if keys := c.images.Stubbed(); len(keys) > 0 {
			if err := c.st.clearImageKeys(ctx, keys); err != nil {
				return err
			}
		}
		rs, urls, err := c.st.missingPosters(ctx, now.Add(-imagesKeepFor))
		if err != nil {
			return err
		}
		kps := make([]int, len(rs))
		if c.ratings != nil && len(rs) > 0 {
			keys := make([]string, len(rs))
			for i, r := range rs {
				keys[i] = r.Tracker + ":" + r.TopicID
			}
			ratings, err := c.ratings.For(ctx, keys)
			if err != nil {
				return err
			}
			for i, r := range rs {
				if kps[i] = r.KinopoiskID; kps[i] == 0 {
					kps[i] = ratings[keys[i]].KinopoiskID
				}
			}
		} else {
			for i, r := range rs {
				kps[i] = r.KinopoiskID
			}
		}
		order := make([]int, len(rs))
		for i := range order {
			order[i] = i
		}
		c.mu.Lock()
		slices.SortStableFunc(order, func(a, b int) int { return b2i(c.forced[rs[b].ID]) - b2i(c.forced[rs[a].ID]) })
		c.mu.Unlock()
		for _, i := range order {
			r := rs[i]
			if budget == 0 {
				break
			}
			if (urls[i] == "" && (kps[i] == 0 || c.kpPoster == nil)) || !c.due("poster", r.ID, now) {
				continue
			}
			budget--
			key := c.fetchPoster(ctx, urls[i], kps[i])
			if ctx.Err() != nil {
				return nil
			}
			if key == "" {
				c.failed("poster", r.ID, now)
				continue
			}
			c.succeeded("poster", r.ID)
			c.noteKPPoster(r.ID, kps[i], key)
			if err := c.st.saveImageKey(ctx, r.ID, key); err != nil {
				return err
			}
		}
		// Номер нашёлся позже (или Кинопоиск не отдал постер раньше): постер страницы — один раз на
		// постер Кинопоиска (спека 11b, 5.4); не скачался — остаётся постер страницы, повтор по паузе.
		if err := c.switchToKPPosters(ctx, now); err != nil || ctx.Err() != nil {
			return err
		}
	}
	var trackers []string
	for name, src := range c.sources {
		if _, ok := src.(torrentFetcher); ok && c.configured(name) {
			trackers = append(trackers, name)
		}
	}
	ts, err := c.st.missingTorrents(ctx, trackers, now.Add(-imagesKeepFor))
	if err != nil {
		return err
	}
	for _, r := range ts {
		if budget == 0 {
			break
		}
		if !c.due("torrent", r.ID, now) {
			continue
		}
		budget--
		tctx, cancel := context.WithTimeout(ctx, torrentWait)
		b, err := c.sources[r.Tracker].(torrentFetcher).Torrent(tctx, r.TopicID)
		cancel()
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			c.failed("torrent", r.ID, now)
			continue
		}
		c.succeeded("torrent", r.ID)
		if err := c.st.saveTorrent(ctx, r.ID, b); err != nil {
			return err
		}
	}
	return nil
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
