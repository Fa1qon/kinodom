package catalog

import (
	"context"
	"database/sql"
	"errors"
	"slices"
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

// ReleaseView — раздача для API.
type ReleaseView struct {
	EntryView
	Description    string `json:"description"`
	TrackerURL     string `json:"trackerUrl"`
	Hash           string `json:"hash"` // infohash; "" — ещё неизвестен
	DetailsPending bool   `json:"detailsPending"`
}

func (r Release) View() ReleaseView {
	return ReleaseView{EntryView: r.Entry.View(), Description: r.Description, TrackerURL: r.TrackerURL,
		Hash: r.InfoHash, DetailsPending: r.DetailsPending}
}

// ReleaseRef — раздача, из которой скачан файл: экран «Загрузки» показывает её название и постер.
type ReleaseRef struct {
	ID       int64  `json:"id"`
	Title    string `json:"title"`
	ImageKey string `json:"imageKey"`
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
	if out.ImageKey == "" && (r.KinopoiskID > 0 || out.Rating.KinopoiskID > 0) {
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
	c.mu.Unlock()
	if ch, ok := c.enrichWake[tracker]; ok {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// nextUrgent — раздача из догрузки вне очереди; false — таких нет.
func (c *Catalog) nextUrgent(ctx context.Context, tracker string) (row, bool, error) {
	for {
		c.mu.Lock()
		ids := c.urgent[tracker]
		if len(ids) == 0 {
			c.mu.Unlock()
			return row{}, false, nil
		}
		id := ids[0]
		c.urgent[tracker] = ids[1:]
		c.mu.Unlock()
		rs, err := c.st.rowsByID(ctx, []int64{id})
		if err != nil {
			return row{}, false, err
		}
		if r, ok := rs[id]; ok && r.DetailsAt.IsZero() {
			return r, true, nil
		}
	}
}

// Постеры Кинопоиска про запас (спека, раздел 8; хвост 5c): номер фильма нашёлся очередью рейтингов
// позже, чем догрузилась страница раздачи, а картинки на ней нет или её хостинг мёртв.
const (
	postersEvery = 10 * time.Minute
	posterRetry  = 24 * time.Hour // постер Кинопоиска не скачался — не раньше чем через сутки
)

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

// fixPosters — постер Кинопоиска раздачам без картинки, у которых номер фильма известен: из
// описания или найден очередью рейтингов. Ошибка — только у базы.
func (c *Catalog) fixPosters(ctx context.Context) error {
	if c.images == nil || c.kpPoster == nil || c.ratings == nil {
		return nil
	}
	now := c.now()
	rs, err := c.st.missingPosters(ctx, now.Add(-imagesKeepFor))
	if err != nil || len(rs) == 0 {
		return err
	}
	keys := make([]string, len(rs))
	for i, r := range rs {
		keys[i] = r.Tracker + ":" + r.TopicID
	}
	ratings, err := c.ratings.For(ctx, keys)
	if err != nil {
		return err
	}
	for i, r := range rs {
		kp := r.KinopoiskID
		if kp == 0 {
			kp = ratings[keys[i]].KinopoiskID
		}
		c.mu.Lock()
		tried := c.posterTried[r.ID]
		c.mu.Unlock()
		if kp == 0 || now.Sub(tried) < posterRetry {
			continue
		}
		key, err := c.images.Fetch(ctx, c.kpPoster(kp), meta.Direct)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			c.mu.Lock()
			c.posterTried[r.ID] = now
			c.mu.Unlock()
			continue
		}
		if err := c.st.saveImageKey(ctx, r.ID, key); err != nil {
			return err
		}
	}
	return nil
}
