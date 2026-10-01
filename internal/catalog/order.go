package catalog

import (
	"context"
	"runtime/debug"
	"slices"

	"kinodom/internal/source"
)

// Порядок раздела (план 14Б). «Раздающие» — место в разделе, как всегда (catalog_entries, порции глубже
// сотни — deep.go). Другие порядки — список порядка раздела в памяти: номера раздач по местам. Rutracker:
// качающие и новизна — из полного списка раздела API (он уже в памяти для порций), число скачиваний — поиском
// форума (со входом); Rutor — страницы раздела в порядке сайта. Обновление раздела списки сбрасывает.

// orderRank — порядки в переключателе пульта.
var orderRank = []string{source.OrderSeeders, source.OrderLeechers, source.OrderNew, source.OrderDownloads}

var orderNames = map[string]string{
	source.OrderSeeders: "Раздающие", source.OrderLeechers: "Качающие", source.OrderNew: "Новые", source.OrderDownloads: "Скачивания",
}

// OrderView — порядок раздела в переключателе пульта.
type OrderView struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// sortedPager — источник отдаёт страницы раздела в своём порядке (Rutor: качающие и новизна; Rutracker:
// число скачиваний — со входом). SortOrders — какие порядки отдаёт сейчас.
type sortedPager interface {
	SortOrders() []string
	SortedPage(ctx context.Context, forums []string, order string, page int) ([]source.Release, bool, error)
}

// Orders — порядки разделов трекера, первым — раздающие.
func (c *Catalog) Orders(tracker string) []string {
	src, ok := c.sources[tracker]
	if !ok {
		return nil
	}
	have := map[string]bool{source.OrderSeeders: true}
	if tracker == "rutracker" {
		have[source.OrderLeechers], have[source.OrderNew] = true, true
	}
	if sp, ok := src.(sortedPager); ok {
		for _, o := range sp.SortOrders() {
			have[o] = true
		}
	}
	var out []string
	for _, o := range orderRank {
		if have[o] {
			out = append(out, o)
		}
	}
	return out
}

// SetDefaultOrder — порядок разделов по умолчанию из настроек (catalog.order).
func (c *Catalog) SetDefaultOrder(o string) {
	c.mu.Lock()
	c.defaultOrder = o
	c.mu.Unlock()
}

// order — порядок раздела трекера: asked, если трекер его отдаёт; иначе — умолчание из настроек, если
// отдаёт; иначе раздающие.
func (c *Catalog) order(tracker, asked string) string {
	have := c.Orders(tracker)
	if slices.Contains(have, asked) {
		return asked
	}
	c.mu.Lock()
	def := c.defaultOrder
	c.mu.Unlock()
	if slices.Contains(have, def) {
		return def
	}
	return source.OrderSeeders
}

// orderViews — порядки трекера для пульта.
func (c *Catalog) orderViews(tracker string) []OrderView {
	out := []OrderView{}
	for _, o := range c.Orders(tracker) {
		out = append(out, OrderView{ID: o, Name: orderNames[o]})
	}
	return out
}

type orderKey struct {
	cat CategoryRef
	ord string
}

// orderList — раздел в порядке: номера раздач по местам; src — список раздела Rutracker в этом порядке
// (из списка API); pos — курсор порций у трекера (место в src или страница сайта); end — список кончился;
// empty — порций подряд без новых раздач.
type orderList struct {
	ids   []int64
	in    map[int64]bool
	src   []source.Release
	pos   int
	end   bool
	empty int
}

// fromSectionList — порядок берётся из списка раздела API в памяти (Rutracker: качающие, новизна).
func fromSectionList(k orderKey) bool {
	return k.cat.Tracker == "rutracker" && (k.ord == source.OrderLeechers || k.ord == source.OrderNew)
}

// sortedBy — список раздела по убыванию поля порядка (стабильно: равные — по раздающим, как пришли).
func sortedBy(rs []source.Release, ord string) []source.Release {
	out := slices.Clone(rs)
	slices.SortStableFunc(out, func(a, b source.Release) int {
		if ord == source.OrderNew {
			return b.Added.Compare(a.Added)
		}
		return b.Leechers - a.Leechers
	})
	return out
}

// ensureOrder — следующая порция списка порядка с трекера. more — у трекера, возможно, есть ещё. Конец —
// список кончился, страница неполная или три порции подряд без новых раздач (сайт повторяет страницу).
// В списке — раздачи с раздающими, как у порций глубже сотни.
func (c *Catalog) ensureOrder(ctx context.Context, k orderKey) (more bool, err error) {
	src, ok := c.sources[k.cat.Tracker]
	if !ok {
		return false, nil
	}
	c.mu.Lock()
	ol := c.orders[k]
	if ol == nil {
		ol = &orderList{in: map[int64]bool{}}
		c.orders[k] = ol
	}
	end, pos, list := ol.end, ol.pos, ol.src
	c.mu.Unlock()
	if end {
		return false, nil
	}
	var rs []source.Release
	last := false
	switch sp, paged := src.(sortedPager); {
	case fromSectionList(k):
		if list == nil {
			c.mu.Lock()
			d, have := c.deep[k.cat]
			c.mu.Unlock()
			if !have {
				if _, err := c.sectionTop(ctx, k.cat, src); err != nil {
					return true, err
				}
				c.mu.Lock()
				d = c.deep[k.cat]
				c.mu.Unlock()
			}
			list = sortedBy(d.rs, k.ord)
			c.mu.Lock()
			ol.src = list
			c.mu.Unlock()
		}
		from := min(pos, len(list))
		to := min(from+topSize, len(list))
		rs, last, pos = list[from:to], to >= len(list), to
	case paged:
		got, more, err := sp.SortedPage(ctx, c.sectionForums(ctx, k.cat), k.ord, pos)
		if err != nil {
			return true, err
		}
		rs = withSeeders(slices.Clone(got))
		last = !more || len(got) == 0
		pos++
	default:
		last = true
	}
	ids, err := c.st.saveFound(ctx, rs, c.now())
	if err != nil {
		return false, dbError{err}
	}
	c.mu.Lock()
	added := 0
	for _, id := range ids {
		if !ol.in[id] {
			ol.in[id] = true
			ol.ids = append(ol.ids, id)
			added++
		}
	}
	ol.pos = pos
	if added == 0 {
		ol.empty++
	} else {
		ol.empty = 0
	}
	if last || ol.empty >= 3 {
		ol.end, last = true, true
	}
	c.mu.Unlock()
	return !last, nil
}

// orderPage — карточки списка порядка после места after: тем же путём, что раздел (склейка дублей и
// фильмов, место карточки — наименьшее место её раздач).
func (c *Catalog) orderPage(ctx context.Context, k orderKey, after, limit int) (entries []Entry, next, rest int, err error) {
	c.mu.Lock()
	var ids []int64
	if ol := c.orders[k]; ol != nil {
		ids = slices.Clone(ol.ids)
	}
	c.mu.Unlock()
	byID, err := c.st.liveRowsByID(ctx, ids)
	if err != nil {
		return nil, after, 0, err
	}
	rows := make([]row, 0, len(ids))
	for i, id := range ids {
		r, ok := byID[id]
		if !ok {
			continue // ушла с трекера
		}
		r.Section, r.Pos = k.cat.ID, i
		if r.CategoryID == "" {
			r.CategoryID = k.cat.ID
		}
		rows = append(rows, r)
	}
	return c.pageOf(ctx, rows, after, limit, true)
}

// orderEnded — список порядка у трекера кончился.
func (c *Catalog) orderEnded(k orderKey) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	ol := c.orders[k]
	return ol != nil && ol.end
}

// orderLock — замок списка порядка: порция с трекера качается одна за раз.
func (c *Catalog) orderLock(k orderKey) chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	l, ok := c.orderBusy[k]
	if !ok {
		l = make(chan struct{}, 1)
		c.orderBusy[k] = l
	}
	return l
}

// prefetchOrder — следующая порция списка порядка в фоне (пульт её не ждёт); уже качается — ничего.
func (c *Catalog) prefetchOrder(k orderKey) {
	l := c.orderLock(k)
	select {
	case l <- struct{}{}:
	default:
		return
	}
	c.mu.Lock()
	base := c.runCtx
	c.mu.Unlock()
	if base == nil {
		base = context.Background()
	}
	c.deepWG.Add(1)
	go func() {
		defer c.deepWG.Done()
		defer func() { <-l }()
		defer func() {
			if p := recover(); p != nil {
				c.log.Error("каталог: паника при подкачке порядка раздела", "tracker", k.cat.Tracker, "section", k.cat.ID, "order", k.ord, "panic", p, "stack", string(debug.Stack()))
			}
		}()
		if _, err := c.ensureOrder(base, k); err != nil && base.Err() == nil {
			c.log.Info("каталог: порция порядка раздела не подкачалась", "tracker", k.cat.Tracker, "section", k.cat.ID, "order", k.ord, "err", err)
		}
	}()
}

// orderFetch — порция списка порядка для просьбы, которой показать нечего: ждёт идущую подкачку (с
// отменой), потом need — нужна ли ещё порция; нужна — качает сама.
func (c *Catalog) orderFetch(ctx context.Context, k orderKey, need func() (bool, error)) (bool, error) {
	l := c.orderLock(k)
	select {
	case l <- struct{}{}:
	case <-ctx.Done():
		return true, ctx.Err()
	}
	defer func() { <-l }()
	ok, err := need()
	if err != nil {
		return true, err
	}
	if !ok {
		return !c.orderEnded(k), nil
	}
	return c.ensureOrder(ctx, k)
}

// resetOrders — раздел обновили: списки порядков — заново.
func (c *Catalog) resetOrders(cat CategoryRef) {
	c.mu.Lock()
	for k := range c.orders {
		if k.cat == cat {
			delete(c.orders, k)
		}
	}
	c.mu.Unlock()
}
