package catalog

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"kinodom/internal/source"
	"kinodom/internal/store"
)

var ctx = context.Background()

// fakeSource — трекер в памяти: топы, страницы раздач, .torrent, лента, поиск. Считает вызовы.
type fakeSource struct {
	name string

	mu           sync.Mutex
	top          map[string][]source.Release // раздел → топ
	topErr       error
	pageErr      error                     // ошибка страницы топа (порции глубже первой сотни)
	topErrs      map[string]error          // ошибка топа одного форума
	repeatAfter  int                       // > 0 — страницы топа дальше этой повторяют её
	details      map[string]source.Details // номер → страница
	detailsErr   map[string]error
	torrents     map[string][]byte
	recent       map[string][]source.Release
	search       []source.Release
	searchErr    error
	searchBlock  chan struct{}     // если задан — поиск ждёт его закрытия (или отмены)
	torrentBlock chan struct{}     // если задан — .torrent ждёт его закрытия (или отмены)
	tree         []source.Category // дерево разделов; nil — по разделам топов, без вложенности
	off          bool              // адрес трекера не введён (этап 11a)
	calls        map[string]int
	atOnce       int                                    // DetailsAtOnce: страниц раздач одновременно; 0 — как у источника без признака
	detailsBlock chan struct{}                          // если задан — страница раздачи ждёт его закрытия (или отмены)
	detailsPanic map[string]bool                        // номер → разбор страницы падает паникой
	pageBlock    chan struct{}                          // если задан — страница раздела (TopPage) ждёт его закрытия (или отмены)
	pagePanic    bool                                   // разбор страницы раздела падает паникой
	inDetails    int                                    // страниц качается сейчас
	maxDetails   int                                    // больше всего одновременно
	sortOrders   []string                               // порядки раздела, которые отдаёт сам трекер (план 14Б)
	sorted       map[string]map[string][]source.Release // порядок → раздел (первый форум) → список
	sortedErr    error                                  // ошибка страницы порядка (трекер не ответил)
	sortedErrs   map[string]error                       // форум → ошибка его страницы порядка, один раз (план 15А)
	sortedBlock  map[string]chan struct{}               // форум → страница порядка ждёт закрытия (или отмены)
	sortedRepeat bool                                   // за концом списка — повтор последней полной страницы (как Rutor)
	sortedPage   int                                    // строк на странице порядка; 0 — 100 (у Rutracker — 50)
	sortedDown   map[string]bool                        // форум → его страница порядка не приходит никогда
}

func (f *fakeSource) SortOrders() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.sortOrders)
}

// SortedPage — страница раздела в порядке order по 100 (как Rutor); forums — раздел или форумы подраздела.
func (f *fakeSource) SortedPage(ctx context.Context, forums []string, order string, page int) ([]source.Release, bool, error) {
	f.mu.Lock()
	f.calls["sorted:"+order]++
	f.calls["sortedForums:"+strings.Join(forums, ",")]++
	var block chan struct{}
	var snapshot []source.Release // данные на момент запроса: ответ, пришедший позже, — про них (ревью 15А)
	if len(forums) > 0 {
		block = f.sortedBlock[forums[0]]
		snapshot = slices.Clone(f.sorted[order][forums[0]])
	}
	f.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return nil, false, ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sortedErr != nil {
		return nil, false, f.sortedErr
	}
	if len(forums) > 0 {
		if err, ok := f.sortedErrs[forums[0]]; ok {
			delete(f.sortedErrs, forums[0])
			return nil, false, err
		}
		if f.sortedDown[forums[0]] {
			return nil, false, errors.New("форум не отвечает")
		}
	}
	if !slices.Contains(f.sortOrders, order) || len(forums) == 0 {
		return nil, false, fmt.Errorf("порядок %s не поддерживается", order)
	}
	all := snapshot
	size := f.sortedPage
	if size == 0 {
		size = 100
	}
	if f.sortedRepeat && page*size >= len(all) && len(all) >= size {
		last := (len(all)/size - 1) * size
		return slices.Clone(all[last : last+size]), true, nil
	}
	from := min(page*size, len(all))
	to := min(from+size, len(all))
	return slices.Clone(all[from:to]), to-from == size, nil
}

func (f *fakeSource) DetailsAtOnce() int { f.mu.Lock(); defer f.mu.Unlock(); return f.atOnce }

func (f *fakeSource) MaxDetails() int { f.mu.Lock(); defer f.mu.Unlock(); return f.maxDetails }

func newFake(name string) *fakeSource {
	return &fakeSource{name: name, top: map[string][]source.Release{}, details: map[string]source.Details{},
		detailsErr: map[string]error{}, torrents: map[string][]byte{}, recent: map[string][]source.Release{}, calls: map[string]int{}}
}

func (f *fakeSource) Name() string { return f.name }

func (f *fakeSource) Configured() bool { f.mu.Lock(); defer f.mu.Unlock(); return !f.off }

func (f *fakeSource) Calls(what string) int { f.mu.Lock(); defer f.mu.Unlock(); return f.calls[what] }

func (f *fakeSource) TopicURL(id string) string { return "https://" + f.name + ".example/topic/" + id }

func (f *fakeSource) set(fn func()) { f.mu.Lock(); fn(); f.mu.Unlock() }

func (f *fakeSource) Categories(context.Context) ([]source.Category, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls["categories"]++
	if f.tree != nil {
		return append([]source.Category(nil), f.tree...), nil
	}
	var out []source.Category
	for id := range f.top {
		out = append(out, source.Category{ID: id, Name: "Раздел " + id})
	}
	return out, nil
}

func (f *fakeSource) Top(_ context.Context, cat string, _ int) ([]source.Release, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls["top"]++
	if f.topErr != nil {
		return nil, f.topErr
	}
	if err := f.topErrs[cat]; err != nil {
		return nil, err
	}
	return append([]source.Release(nil), f.top[cat]...), nil
}

// TopPage — страница топа по 100 (как Rutor): page 0 — первая сотня; pageErr — ошибка страницы.
func (f *fakeSource) TopPage(ctx context.Context, cat string, page int) ([]source.Release, bool, error) {
	f.mu.Lock()
	f.calls["toppage"]++
	block := f.pageBlock
	boom := f.pagePanic
	f.mu.Unlock()
	if boom {
		panic("разбор страницы раздела")
	}
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return nil, false, ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.pageErr != nil {
		return nil, false, f.pageErr
	}
	all := f.top[cat]
	if f.repeatAfter > 0 && page > f.repeatAfter {
		page = f.repeatAfter // за концом списка — снова последняя страница (как может ответить сайт)
	}
	from := min(page*100, len(all))
	to := min(from+100, len(all))
	return append([]source.Release(nil), all[from:to]...), to-from == 100, nil
}

func (f *fakeSource) Details(ctx context.Context, id string) (source.Details, error) {
	f.mu.Lock()
	f.calls["details"]++
	f.calls["details:"+id]++
	block := f.detailsBlock
	f.inDetails++
	f.maxDetails = max(f.maxDetails, f.inDetails)
	f.mu.Unlock()
	defer func() { f.mu.Lock(); f.inDetails--; f.mu.Unlock() }()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return source.Details{}, ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.detailsPanic[id] {
		panic("разбор страницы " + id)
	}
	if err := f.detailsErr[id]; err != nil {
		return source.Details{}, err
	}
	d, ok := f.details[id]
	if !ok {
		return source.Details{}, fmt.Errorf("%s: раздача %s: %w", f.name, id, source.ErrRemoved)
	}
	return d, nil
}

func (f *fakeSource) Torrent(ctx context.Context, id string) ([]byte, error) {
	f.mu.Lock()
	f.calls["torrent"]++
	block := f.torrentBlock
	b, ok := f.torrents[id]
	f.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if ok {
		return b, nil
	}
	return nil, errors.New("нет .torrent")
}

func (f *fakeSource) Recent(_ context.Context, cat string) ([]source.Release, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls["recent"]++
	return f.recent[cat], nil
}

func (f *fakeSource) Search(ctx context.Context, q string) ([]source.Release, error) {
	f.mu.Lock()
	f.calls["search"]++
	f.calls["search:"+q]++
	block, rs, err := f.searchBlock, f.search, f.searchErr
	f.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return rs, err
}

// rel — строка топа.
func rel(tracker, id, title string, seeders int, size int64, infohash string) source.Release {
	return source.Release{Tracker: tracker, TopicID: id, Title: title, Seeders: seeders, Size: size, InfoHash: infohash}
}

// clock — подменные часы каталога.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

func openDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "kinodom.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// newCatalog — каталог на фейковых трекерах с разделами rutor:12 и rutracker:2110.
func newCatalog(t *testing.T, db *store.DB, mod func(*Options), srcs ...*fakeSource) (*Catalog, *clock) {
	t.Helper()
	o := Options{DB: db, Sections: []Section{{"rutor", "12", false}, {"rutracker", "2110", false}}}
	for _, s := range srcs {
		o.Sources = append(o.Sources, s)
	}
	if mod != nil {
		mod(&o)
	}
	c := New(o)
	clk := &clock{t: time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)}
	c.now = clk.now
	return c, clk
}

func refresh(t *testing.T, c *Catalog, force bool) time.Duration {
	t.Helper()
	wait, err := c.refreshPass(ctx, force)
	if err != nil {
		t.Fatal(err)
	}
	return wait
}

func list(t *testing.T, c *Catalog, o ListOptions) []Entry {
	t.Helper()
	es, _, err := c.List(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	return es
}

func problemText(t *testing.T, db *store.DB, id string) string {
	t.Helper()
	ps, err := db.Problems(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range ps {
		if p.ID == id {
			return p.Text
		}
	}
	return ""
}

// enrichAll догружает, пока есть что.
func enrichAll(t *testing.T, c *Catalog, tracker string) {
	t.Helper()
	for i := 0; ; i++ {
		did, err := c.enrichStep(ctx, tracker)
		if err != nil {
			t.Fatal(err)
		}
		if !did {
			c.posterWG.Wait() // постеры качаются вне шага
			return
		}
		if i > 200 {
			t.Fatal("догрузка не кончается")
		}
	}
}
