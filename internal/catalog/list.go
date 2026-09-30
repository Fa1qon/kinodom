package catalog

import (
	"cmp"
	"context"
	"slices"
	"time"

	"kinodom/internal/meta"
)

// Entry — карточка каталога или поиска.
type Entry struct {
	ID         int64 // номер раздачи в Kinodom (/releases/{id}, этап 7)
	Tracker    string
	TopicID    string
	Title      string // "" — название ещё не загружено (топ Rutracker приходит без названий)
	Quality    string // из названия: «WEB-DL 1080p»
	CategoryID string
	Category   string // название раздела
	Seeders    int
	Leechers   int
	Size       int64
	Added      time.Time
	InfoHash   string
	ImageKey   string      // картинка: /img/{ImageKey}; "" — нет
	Rating     meta.Rating // KinopoiskID = 0 — фильм не найден (или ещё не искали)
	Format     string      // «MKV», «AVI, MKV»; "" — неизвестен (спека этапа 7, раздел 10.2)
	Variants   int         // раздач этого фильма на обоих трекерах — у карточки каталога; 0 — не считали
	// DetailsPending — страницу раздачи ещё не загружали: формата и номера Кинопоиска может не быть.
	DetailsPending bool
}

type ListOptions struct {
	Tracker  string // "" — все трекеры
	Category string // "" — все включённые разделы; иначе номер раздела трекера Tracker
	Offset   int
	Limit    int // 0 — 50
}

// List — основной каталог: все включённые разделы вместе, по убыванию раздающих, с фильтром по
// разделу (спека, раздел 7). Одинаковый infohash с двух трекеров — одна карточка с большим числом
// раздающих; раздачи одного фильма — одна карточка, Variants — сколько их (спека этапа 7, раздел 10.4).
// total — сколько всего карточек под фильтром.
func (c *Catalog) List(ctx context.Context, o ListOptions) (entries []Entry, total int, err error) {
	if o.Limit <= 0 {
		o.Limit = 50
	}
	o.Offset = max(o.Offset, 0) // отрицательное смещение уронило бы срез (ревью 5c)
	rs, err := c.st.catalogRows(ctx, c.enabled())
	if err != nil {
		return nil, 0, err
	}
	var filtered []row
	for _, r := range rs {
		if (o.Tracker == "" || r.Tracker == o.Tracker) && (o.Category == "" || r.CategoryID == o.Category) {
			filtered = append(filtered, r)
		}
	}
	filtered = collapse(filtered)
	kp, err := c.kinopoiskIDs(ctx, filtered)
	if err != nil {
		return nil, 0, err
	}
	filtered, size := films(filtered, kp, c.PreferredFormat())
	total = len(filtered)
	if o.Offset >= total {
		return []Entry{}, total, nil
	}
	page := filtered[o.Offset:min(total, o.Offset+o.Limit)]
	if entries, err = c.entries(ctx, page); err != nil {
		return nil, 0, err
	}
	var ids []int
	for _, r := range page {
		if id := kp[r.ID]; id > 0 {
			ids = append(ids, id)
		}
	}
	vs, err := c.variantRows(ctx, ids)
	if err != nil {
		return nil, 0, err
	}
	for i, r := range page {
		entries[i].Variants = max(1, len(vs[kp[r.ID]]), size[r.ID])
	}
	return entries, total, nil
}

// collapse — одна карточка на infohash (спека, раздел 7): остаётся запись с большим числом
// раздающих. Раздачи без infohash не схлопываются. Порядок — по убыванию раздающих.
func collapse(rs []row) []row {
	best := map[string]int{} // infohash → индекс в out
	out := make([]row, 0, len(rs))
	for _, r := range rs {
		if r.InfoHash == "" {
			out = append(out, r)
			continue
		}
		if i, ok := best[r.InfoHash]; ok {
			if r.Seeders > out[i].Seeders {
				out[i] = r
			}
			continue
		}
		best[r.InfoHash] = len(out)
		out = append(out, r)
	}
	slices.SortStableFunc(out, func(a, b row) int { return b.Seeders - a.Seeders })
	return out
}

// entries — карточки: название раздела, качество из названия, рейтинг.
func (c *Catalog) entries(ctx context.Context, rs []row) ([]Entry, error) {
	var ratings map[string]meta.Rating
	if c.ratings != nil && len(rs) > 0 {
		keys := make([]string, len(rs))
		for i, r := range rs {
			keys[i] = r.Tracker + ":" + r.TopicID
		}
		var err error
		if ratings, err = c.ratings.For(ctx, keys); err != nil {
			return nil, err
		}
	}
	names, err := c.st.categoryNames(ctx, rs)
	if err != nil {
		return nil, err
	}
	out := make([]Entry, len(rs))
	for i, r := range rs {
		out[i] = Entry{ID: r.ID, Tracker: r.Tracker, TopicID: r.TopicID, Title: r.Title, Quality: meta.ParseTitle(r.Title).Quality,
			CategoryID: r.CategoryID, Category: cmp.Or(names[CategoryRef{r.Tracker, r.CategoryID}], r.CategoryID), Seeders: r.Seeders,
			Leechers: r.Leechers, Size: r.Size, Added: r.Added, InfoHash: r.InfoHash, ImageKey: r.ImageKey, Format: r.Format,
			DetailsPending: r.DetailsAt.IsZero(), Rating: ratings[r.Tracker+":"+r.TopicID]}
		if out[i].Rating.KinopoiskID == 0 && r.KinopoiskID > 0 {
			out[i].Rating.KinopoiskID = r.KinopoiskID // номер из описания — до очереди рейтингов (медиатека, 11b-Б)
		}
	}
	return out, nil
}

// Category — раздел каталога для фильтра.
type Category struct {
	Tracker string
	ID      string
	Name    string
	Count   int // карточек в разделе
}

// Categories — включённые разделы с названиями и числом карточек (фильтр каталога).
func (c *Catalog) Categories(ctx context.Context) ([]Category, error) {
	cats := c.enabled()
	rs, err := c.st.catalogRows(ctx, cats)
	if err != nil {
		return nil, err
	}
	// Карточки считаются, как их показывает каталог: по трекерам отдельно, одинаковый infohash
	// схлопнут внутри трекера (спека этапа 7, раздел 3).
	byTracker := map[string][]row{}
	for _, r := range rs {
		byTracker[r.Tracker] = append(byTracker[r.Tracker], r)
	}
	count := map[CategoryRef]int{}
	for _, trs := range byTracker {
		for _, r := range collapse(trs) {
			count[CategoryRef{r.Tracker, r.CategoryID}]++
		}
	}
	var refs []row
	for _, cat := range cats {
		refs = append(refs, row{Tracker: cat.Tracker, CategoryID: cat.ID})
	}
	names, err := c.st.categoryNames(ctx, refs)
	if err != nil {
		return nil, err
	}
	out := make([]Category, 0, len(cats))
	for _, cat := range cats {
		if _, ok := c.sources[cat.Tracker]; !ok {
			continue
		}
		out = append(out, Category{Tracker: cat.Tracker, ID: cat.ID, Name: cmp.Or(names[cat], cat.ID), Count: count[cat]})
	}
	return out, nil
}

// UpdatedAt — последнее удачное обновление разделов трекера в каталоге; нуль — ещё не было.
func (c *Catalog) UpdatedAt(ctx context.Context, tracker string) (time.Time, error) {
	var last time.Time
	for _, cat := range c.enabled() {
		if cat.Tracker != tracker {
			continue
		}
		_, at, err := c.st.state(ctx, cat)
		if err != nil {
			return time.Time{}, err
		}
		if at.After(last) {
			last = at
		}
	}
	return last, nil
}
