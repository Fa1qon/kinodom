package catalog

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"kinodom/internal/meta"
)

// Entry — карточка каталога или поиска.
type Entry struct {
	ID         int64 // номер раздачи в Kinodom (/releases/{id}, этап 7)
	Tracker    string
	TopicID    string
	Title      string // "" — название ещё не загружено (топ t2 приходит без названий)
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
	// Preferred — формат в приоритете (основной формат раздачи совпадает с настройкой; план 14А): пульт
	// подсвечивает формат.
	Preferred bool
	// PreferredAlt — у карточки, чья раздача не в формате в приоритете, а другая раздача фильма — в нём:
	// этот формат («MKV»), пульт показывает его отдельной меткой (ревью 14А, Important 2); иначе "".
	PreferredAlt string
	// DetailsPending — страницу раздачи ещё не загружали: формата и номера Кинопоиска может не быть.
	DetailsPending bool
	Downloads      int // сколько раз скачана; 0 — неизвестно (план 14Б)
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
		if (o.Tracker == "" || r.Tracker == o.Tracker) && (o.Category == "" || r.Section == o.Category) {
			filtered = append(filtered, r)
		}
	}
	// Один раздел (так смотрит пульт) — по месту в разделе: первая сотня стоит по раздающим с обновления,
	// порции глубже — в конце. Иначе порция t1 (раздающих он считает неточно) двигала бы уже показанные
	// страницы: карточки повторялись бы и терялись (вживую 11b-Г).
	inSection := o.Tracker != "" && o.Category != ""
	filtered = c.collapse(filtered)
	kp, err := c.kinopoiskIDs(ctx, filtered)
	if err != nil {
		return nil, 0, err
	}
	filtered, size, _ := films(filtered, kp, c.PreferredFormat(), !inSection, false)
	total = len(filtered)
	if o.Offset >= total {
		return []Entry{}, total, nil
	}
	entries, err = c.cards(ctx, filtered[o.Offset:min(total, o.Offset+o.Limit)], kp, size)
	return entries, total, err
}

// NewReleases — последние раздачи каталога, сгруппированные по произведению.
// group — «our-film», «our-series», «foreign-film» или «foreign-series».
// Сначала учитывается дата добавления на трекер, затем номер записи.
// Для одного произведения остаётся самая свежая раздача, чтобы ряд не заполняли разные рипы.
func (c *Catalog) NewReleases(ctx context.Context, group string, limit int) ([]Entry, error) {
	if limit <= 0 {
		limit = 6
	}
	limit = min(limit, PageSize)
	if group != "our-film" && group != "our-series" && group != "foreign-film" && group != "foreign-series" {
		return nil, fmt.Errorf("неизвестная группа новинок %q", group)
	}
	rs, err := c.st.catalogRows(ctx, c.enabled())
	if err != nil {
		return nil, err
	}
	series := strings.HasSuffix(group, "series")
	names, err := c.st.categoryNames(ctx, rs)
	if err != nil {
		return nil, err
	}
	seriesByCategory, err := c.seriesFlags(ctx, rs)
	if err != nil {
		return nil, err
	}
	filtered := make([]row, 0, len(rs))
	seenHash := map[string]bool{}
	for _, r := range rs {
		isSeries := seriesByCategory[CategoryRef{r.Tracker, r.CategoryID}] || meta.ParseTitle(r.Title).Series
		if r.Title == "" || r.Added.IsZero() || isSeries != series || releaseGroup(r, names, isSeries) != group {
			continue
		}
		// Один и тот же infohash может быть в нескольких активных разделах или на двух трекерах.
		// Ряд новинок показывает его один раз, сохраняя первую (самую новую) запись.
		if r.InfoHash != "" && seenHash[r.InfoHash] {
			continue
		}
		if r.InfoHash != "" {
			seenHash[r.InfoHash] = true
		}
		filtered = append(filtered, r)
	}
	slices.SortStableFunc(filtered, func(a, b row) int {
		if d := b.Added.Compare(a.Added); d != 0 {
			return d
		}
		switch {
		case b.ID > a.ID:
			return 1
		case b.ID < a.ID:
			return -1
		default:
			return 0
		}
	})
	// films() уже умеет связывать раздачи по номеру Кинопоиска и ключу названия. Позиция здесь
	// задана свежестью, поэтому byPlace оставляет самую новую раздачу произведения.
	for i := range filtered {
		filtered[i].Pos = i
	}
	kp, err := c.kinopoiskIDs(ctx, filtered)
	if err != nil {
		return nil, err
	}
	grouped, size, _ := films(filtered, kp, c.PreferredFormat(), false, true)
	if len(grouped) > limit {
		grouped = grouped[:limit]
	}
	return c.cards(ctx, grouped, kp, size)
}

// releaseGroup связывает раздачу с четырьмя вкладками главной. Для стандартных разделов используем
// стабильные идентификаторы трекеров; названия категорий покрывают вручную выбранные подразделы.
func releaseGroup(r row, names map[CategoryRef]string, series bool) string {
	name := strings.ToLower(names[CategoryRef{r.Tracker, r.CategoryID}])
	our := strings.Contains(name, "наше") || strings.Contains(name, "русск") || strings.Contains(name, "россий") || strings.Contains(name, "отечеств") || strings.Contains(name, "советск")
	foreign := strings.Contains(name, "зарубеж") || strings.Contains(name, "иностран")
	if !our && !foreign {
		switch r.Tracker {
		case "t1":
			our = r.CategoryID == "5" || r.CategoryID == "16"
			foreign = r.CategoryID == "1" || r.CategoryID == "4" || r.CategoryID == "17"
		case "t2":
			our = r.Section == "22" || r.Section == "9"
			foreign = r.Section == "7" || r.Section == "189"
		}
	}
	if !our && !foreign {
		// Неопознанные видеокатегории остаются в зарубежной вкладке, чтобы новинка не пропадала.
		foreign = true
	}
	if our {
		if series {
			return "our-series"
		}
		return "our-film"
	}
	if series {
		return "foreign-series"
	}
	return "foreign-film"
}

// seriesFlags classifies rows by both title and tracker category. A series release often has no
// S01/E01 marker in its title, while the category tree still identifies it unambiguously. Load the
// t2 tree once for the whole request instead of querying it once per release.
func (c *Catalog) seriesFlags(ctx context.Context, rs []row) (map[CategoryRef]bool, error) {
	out := make(map[CategoryRef]bool, len(rs))
	needt2 := false
	for _, r := range rs {
		if r.Tracker == "t2" {
			needt2 = true
			break
		}
	}
	parents := map[string]string{}
	if needt2 {
		tree, err := c.st.tree(ctx, "t2")
		if err != nil {
			return nil, err
		}
		for _, x := range tree {
			parents[x.ID] = x.ParentID
		}
	}
	for _, r := range rs {
		isSeries := false
		switch r.Tracker {
		case "t1":
			isSeries = t1Series[r.CategoryID]
		case "t2":
			for id, n := r.CategoryID, 0; id != "" && n < 20; id, n = parents[id], n+1 {
				if id == "c18" {
					isSeries = true
					break
				}
			}
		}
		out[CategoryRef{r.Tracker, r.CategoryID}] = out[CategoryRef{r.Tracker, r.CategoryID}] || isSeries
	}
	return out, nil
}

// cards — карточки строк: название раздела, рейтинг, раздач фильма на обоих трекерах.
func (c *Catalog) cards(ctx context.Context, page []row, kp map[int64]int, size map[int64]int) ([]Entry, error) {
	entries, err := c.entries(ctx, page)
	if err != nil {
		return nil, err
	}
	var ids []int
	for _, r := range page {
		if id := kp[r.ID]; id > 0 {
			ids = append(ids, id)
		}
	}
	vs, err := c.variantRows(ctx, ids)
	if err != nil {
		return nil, err
	}
	pref := c.PreferredFormat()
	for i, r := range page {
		entries[i].Variants = max(1, len(vs[kp[r.ID]]), size[r.ID])
		for _, v := range vs[kp[r.ID]] {
			if !entries[i].Preferred && prefers(v.Format, pref) {
				entries[i].PreferredAlt = pref
			}
		}
	}
	return entries, nil
}

// SectionPage — карточки раздела после места after (-1 — с начала), не больше limit, по месту в
// разделе. Курсор, а не смещение: порция с трекера и склейка раздач во время прокрутки не сдвигают
// показанное (ревью 11b-Г). next — место последней карточки (after, если карточек нет); rest — сколько
// карточек раздела дальше неё.
func (c *Catalog) SectionPage(ctx context.Context, tracker, section string, after, limit int) (entries []Entry, next, rest int, err error) {
	rs, err := c.st.catalogRows(ctx, c.enabled())
	if err != nil {
		return nil, after, 0, err
	}
	var filtered []row
	for _, r := range rs {
		if r.Tracker == tracker && r.Section == section {
			filtered = append(filtered, r)
		}
	}
	return c.pageOf(ctx, filtered, after, limit, false)
}

// pageOf — карточки строк раздела (или списка порядка) после места after: склейка дублей и фильмов, место
// карточки — наименьшее место её раздач; byPlace — карточка показывает раздачу этого места (порядки).
func (c *Catalog) pageOf(ctx context.Context, rs []row, after, limit int, byPlace bool) (entries []Entry, next, rest int, err error) {
	rs = c.collapseBy(rs, byPlace)
	kp, err := c.kinopoiskIDs(ctx, rs)
	if err != nil {
		return nil, after, 0, err
	}
	out, size, place := films(rs, kp, c.PreferredFormat(), false, byPlace)
	from := len(out)
	for i, r := range out {
		if place[r.ID] > after {
			from = i
			break
		}
	}
	page := out[from:min(len(out), from+limit)]
	next = after
	if len(page) > 0 {
		next = place[page[len(page)-1].ID]
	}
	entries, err = c.cards(ctx, page, kp, size)
	return entries, next, len(out) - from - len(page), err
}

// collapse — одна карточка на infohash (спека, раздел 7): остаётся раздача со страницей (своего трекера,
// не из источника поиска — спека 11b, раздел 8), из равных — с большим числом раздающих. Раздачи без
// infohash не схлопываются. Порядок — по убыванию раздающих.
func (c *Catalog) collapse(rs []row) []row { return c.collapseBy(rs, false) }

// collapseBy — collapse; byPlace (раздел в порядке, план 14Б) — из равных остаётся раздача меньшего места: она
// и даёт карточке место (ревью 14Б: оставалась с большим числом раздающих, и карточка уезжала ниже).
func (c *Catalog) collapseBy(rs []row, byPlace bool) []row {
	best := map[string]int{} // infohash → индекс в out
	out := make([]row, 0, len(rs))
	for _, r := range rs {
		if r.InfoHash == "" {
			out = append(out, r)
			continue
		}
		if i, ok := best[r.InfoHash]; ok {
			if mine, was := !c.pageless(r), !c.pageless(out[i]); mine != was {
				if mine {
					out[i] = r
				}
				continue
			}
			if byPlace && r.Pos < out[i].Pos || !byPlace && r.Seeders > out[i].Seeders {
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
	pref := c.PreferredFormat()
	for i, r := range rs {
		out[i] = Entry{ID: r.ID, Tracker: r.Tracker, TopicID: r.TopicID, Title: r.Title, Quality: meta.ParseTitle(r.Title).Quality,
			CategoryID: r.CategoryID, Category: cmp.Or(names[CategoryRef{r.Tracker, r.CategoryID}], r.CategoryID), Seeders: r.Seeders,
			Leechers: r.Leechers, Size: r.Size, Added: r.Added, InfoHash: r.InfoHash, ImageKey: r.ImageKey, Format: r.Format, Downloads: r.Downloads,
			DetailsPending: r.DetailsAt.IsZero(), Rating: ratings[r.Tracker+":"+r.TopicID], Preferred: prefers(r.Format, pref)}
		if out[i].Rating.KinopoiskID == 0 && r.KinopoiskID > 0 {
			out[i].Rating.KinopoiskID = r.KinopoiskID // номер из описания — до очереди рейтингов (медиатека, 11b-Б)
		}
	}
	return out, nil
}

// Cards — карточки раздач по номерам, в порядке ids; неизвестные и ушедшие с трекера пропущены. Так пульт
// спрашивает незаконченные карточки у экрана (спека 11b, 14.1): свои без страницы — в догрузку вне очереди
// в том же порядке (что на экране — первым). Variants не считается: метку «N раздач» пульт держит прежнюю.
func (c *Catalog) Cards(ctx context.Context, ids []int64) ([]Entry, error) {
	byID, err := c.st.liveRowsByID(ctx, ids)
	if err != nil {
		return nil, err
	}
	rs := make([]row, 0, len(ids))
	pending := map[string][]int64{}
	var trackers []string
	for _, id := range ids {
		r, ok := byID[id]
		if !ok {
			continue
		}
		rs = append(rs, r)
		if r.DetailsAt.IsZero() && !c.pageless(r) {
			if pending[r.Tracker] == nil {
				trackers = append(trackers, r.Tracker)
			}
			pending[r.Tracker] = append(pending[r.Tracker], r.ID)
		}
	}
	for _, t := range trackers {
		c.enqueueFound(ctx, t, pending[t], cardsLimit)
	}
	return c.entries(ctx, rs)
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
		for _, r := range c.collapse(trs) {
			count[CategoryRef{r.Tracker, r.Section}]++
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
