package catalog

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
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

	mu          sync.Mutex
	top         map[string][]source.Release // раздел → топ
	topErr      error
	details     map[string]source.Details // номер → страница
	detailsErr  map[string]error
	torrents    map[string][]byte
	recent      map[string][]source.Release
	search      []source.Release
	searchErr   error
	searchBlock chan struct{}     // если задан — поиск ждёт его закрытия (или отмены)
	tree        []source.Category // дерево разделов; nil — по разделам топов, без вложенности
	calls       map[string]int
}

func newFake(name string) *fakeSource {
	return &fakeSource{name: name, top: map[string][]source.Release{}, details: map[string]source.Details{},
		detailsErr: map[string]error{}, torrents: map[string][]byte{}, recent: map[string][]source.Release{}, calls: map[string]int{}}
}

func (f *fakeSource) Name() string { return f.name }

func (f *fakeSource) Calls(what string) int { f.mu.Lock(); defer f.mu.Unlock(); return f.calls[what] }

func (f *fakeSource) TopicURL(id string) string { return "https://" + f.name + ".example/topic/" + id }

func (f *fakeSource) set(fn func()) { f.mu.Lock(); fn(); f.mu.Unlock() }

func (f *fakeSource) Categories(context.Context) ([]source.Category, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
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
	return append([]source.Release(nil), f.top[cat]...), nil
}

func (f *fakeSource) Details(_ context.Context, id string) (source.Details, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls["details"]++
	f.calls["details:"+id]++
	if err := f.detailsErr[id]; err != nil {
		return source.Details{}, err
	}
	d, ok := f.details[id]
	if !ok {
		return source.Details{}, fmt.Errorf("%s: раздача %s: %w", f.name, id, source.ErrRemoved)
	}
	return d, nil
}

func (f *fakeSource) Torrent(_ context.Context, id string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls["torrent"]++
	if b, ok := f.torrents[id]; ok {
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
			return
		}
		if i > 200 {
			t.Fatal("догрузка не кончается")
		}
	}
}
