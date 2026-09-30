package catalog

import (
	"context"
	"errors"
	"strconv"
	"sync/atomic"
	"time"

	"kinodom/internal/meta"
	"kinodom/internal/netx"
	"kinodom/internal/source"
)

const (
	detailsRetry = 30 * time.Minute // страница раздачи не загрузилась — повтор
	forumPause   = 10 * time.Minute // форум закрыт проверкой Cloudflare — не ходить (источник помнит неудачу столько же)
	enrichIdle   = time.Minute      // догружать нечего — заглядывать снова
	torrentWait  = 20 * time.Second // .torrent Rutor ждём в шаге не дольше: зависший — повтор в фоне (Х8)
)

// torrentFetcher — источник отдаёт .torrent (Rutor): каталог качает его заранее, чтобы список
// файлов был сразу — иначе цель «картинка ≤ 20 с» не достижима (спека, раздел 7).
type torrentFetcher interface {
	Torrent(ctx context.Context, topicID string) ([]byte, error)
}

// recentFetcher — лента новых раздач без пропуска Cloudflare (Rutracker, Atom): запасной источник
// названий, когда форум закрыт (спека, раздел 6).
type recentFetcher interface {
	Recent(ctx context.Context, forumID string) ([]source.Release, error)
}

// enrichLoop догружает раздачи трекера по одной, в порядке основного каталога: первые экраны
// заполняются первыми (спека, раздел 7).
func (c *Catalog) enrichLoop(ctx context.Context, tracker string) error {
	for {
		did, err := c.enrichStep(ctx, tracker)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			return err // база
		}
		if did {
			continue // темп задают ограничители источника (1 запрос/с)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-c.enrichWake[tracker]:
		case <-time.After(enrichIdle):
		}
	}
}

// enrichStep — одна раздача: страница, постер, .torrent, очередь рейтингов. did = false — делать
// сейчас нечего. Ошибки трекера раздачу откладывают; ошибка — только у базы.
func (c *Catalog) enrichStep(ctx context.Context, tracker string) (bool, error) {
	now := c.now()
	if !c.configured(tracker) || c.forumPausedUntil(tracker).After(now) {
		return false, nil
	}
	// Сначала раздачи, открытые в пульте: их страницу ждёт человек (хвост 5c); потом найденные поиском.
	r, opened, urgent, err := c.nextUrgent(ctx, tracker)
	if err != nil {
		return false, err
	}
	ok := urgent
	if !ok {
		r, ok, err = c.st.nextToEnrich(ctx, tracker, c.enabled(), now)
	}
	if err != nil || !ok {
		return false, err
	}
	src := c.sources[tracker]
	d, err := src.Details(ctx, r.TopicID)
	switch {
	case ctx.Err() != nil:
		return false, nil
	case errors.Is(err, source.ErrRemoved):
		return true, c.st.markRemoved(ctx, r.ID)
	case errors.Is(err, netx.ErrChallenge) || trackerDown(err):
		// Форум закрыт проверкой (пропуск добыть не вышло) или не отвечает сам либо через прокси: не
		// ходить за страницами — иначе каждая из сотен раздач — лишний запрос или таймауты по
		// зеркалам. Названия новых раздач — из ленты (спека, разделы 6, 16). Проблему ставит
		// догрузка: топы Rutracker идут по API, который жив и при лежащем форуме, а к разделу
		// обновление топов заглядывает раз в 6 часов.
		c.pauseForum(tracker, now.Add(forumPause))
		text := err.Error() + " — пока новые раздачи без описаний"
		if _, ok := src.(recentFetcher); ok {
			text += ", названия из ленты"
		}
		c.setProblem(ctx, "catalog."+tracker+".forum", text)
		c.recentTitles(ctx, tracker)
		return true, nil
	case err != nil:
		var pe *source.ParseError
		if errors.As(err, &pe) {
			c.setProblem(ctx, "catalog."+tracker+".parse", err.Error())
		}
		c.log.Warn("каталог: страница раздачи не загрузилась", "tracker", tracker, "topic", r.TopicID, "err", err)
		return true, c.st.detailsFailed(ctx, r.ID, now.Add(detailsRetry))
	}
	c.clearProblem(ctx, "catalog."+tracker+".forum")
	c.clearProblem(ctx, "catalog."+tracker+".parse")
	kpID, _ := strconv.Atoi(d.KinopoiskID)
	// .torrent — до отметки «страница загружена»: экран раздачи перестаёт ждать догрузку и сразу
	// показывает серии Rutor. Постер — после: медленный хостинг не держит экран раздачи
	// (финальное ревью 7a). Найденному поиском .torrent — когда откроют (Release): он идёт через тот
	// же ограничитель «запрос в секунду» и вдвое замедлял бы постеры поиска (11b-А, вживую).
	var torrent []byte
	if tf, ok := src.(torrentFetcher); ok && (!urgent || opened) {
		tctx, cancel := context.WithTimeout(ctx, torrentWait)
		var interrupted atomic.Bool
		if !urgent {
			c.yieldTo(tracker, func() { interrupted.Store(true); cancel() })
		}
		b, err := tf.Torrent(tctx, r.TopicID)
		if !urgent {
			c.yieldTo(tracker, nil)
		}
		cancel()
		switch {
		case err == nil:
			if err := c.st.saveTorrent(ctx, r.ID, b); err != nil {
				return false, err
			}
			torrent = b
		case interrupted.Load():
			// Не сбой: повтор в фоне или сразу при открытии раздачи.
		case ctx.Err() == nil:
			c.log.Warn("каталог: .torrent не скачался — раздача откроется по magnet, повтор позже", "tracker", tracker, "topic", r.TopicID, "err", err)
			c.failed("torrent", r.ID, now)
		}
	}
	if err := c.st.saveDetails(ctx, r.ID, d, kpID, "", c.formatOf(d.Description, torrent), now); err != nil {
		return false, err
	}
	c.posterLater(ctx, r.ID, d.PosterURL, kpID, urgent)
	if c.ratings != nil {
		r.Title, r.KinopoiskID, r.IMDbID = firstNonEmpty(d.Title, r.Title), kpID, d.IMDbID
		pos := 0 // открытую раздачу — в рейтинги первой
		if !urgent {
			if pos, err = c.position(ctx, r.ID); err != nil {
				return false, err
			}
		}
		if err := c.ratings.Enqueue(ctx, pos, ratingItem(r)); err != nil {
			return false, err
		}
	}
	return true, nil
}

// fetchPoster — постер со страницы раздачи (через прокси трекеров), а если его нет или хостинг
// не отдал картинку — постер Кинопоиска (напрямую) (спека, раздел 8). "" — картинки нет.
func (c *Catalog) fetchPoster(ctx context.Context, posterURL string, kpID int) string {
	if c.images == nil {
		return ""
	}
	// Номер известен — постер Кинопоиска: без водяных знаков трекеров и без медленных хостингов (спека
	// 11b, 5.4). Не скачался — постер страницы.
	if kpID > 0 && c.kpPoster != nil {
		if key, err := c.images.Fetch(ctx, c.kpPoster(kpID), meta.Direct); err == nil {
			return key
		}
	}
	if posterURL != "" {
		key, err := c.images.Fetch(ctx, posterURL, meta.ViaProxy)
		if err == nil {
			return key
		}
		c.log.Info("каталог: постер раздачи не скачался", "url", posterURL, "err", err)
	}
	return ""
}

// recentTitles — названия новых раздач из ленты Atom для раздач каталога без названия. Лента
// иногда отвечает 520 (исследование, раздел 10) — повтор при следующей остановке форума.
func (c *Catalog) recentTitles(ctx context.Context, tracker string) {
	rf, ok := c.sources[tracker].(recentFetcher)
	if !ok {
		return
	}
	for _, cat := range c.enabled() {
		if cat.Tracker != tracker {
			continue
		}
		rs, err := rf.Recent(ctx, cat.ID)
		if err != nil {
			c.log.Info("каталог: лента раздела не ответила", "tracker", tracker, "category", cat.ID, "err", err)
			continue
		}
		for _, r := range rs {
			if err := c.st.setTitleIfEmpty(ctx, tracker, r.TopicID, r.Title); err != nil {
				c.log.Error("каталог: название из ленты не записалось", "err", err)
				return
			}
		}
	}
}

// position — место раздачи в основном каталоге: приоритет в очереди рейтингов.
func (c *Catalog) position(ctx context.Context, id int64) (int, error) {
	rs, err := c.st.catalogRows(ctx, c.enabled())
	if err != nil {
		return 0, err
	}
	for i, r := range rs {
		if r.ID == id {
			return i, nil
		}
	}
	return len(rs), nil
}

func (c *Catalog) pauseForum(tracker string, until time.Time) {
	c.mu.Lock()
	c.forumPaused[tracker] = until
	c.mu.Unlock()
}

func (c *Catalog) forumPausedUntil(tracker string) time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.forumPaused[tracker]
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}
