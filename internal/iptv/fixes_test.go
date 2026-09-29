package iptv

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"kinodom/internal/iptv/m3u"
	"kinodom/internal/iptv/probe"
)

// countingChecker — проверка, которая считает, сколько идёт одновременно; panicOn — упасть на ссылке.
type countingChecker struct {
	now, max atomic.Int32
	calls    atomic.Int32
	panicOn  string
}

func (c *countingChecker) run(t probe.Target) probe.Result {
	c.calls.Add(1)
	n := c.now.Add(1)
	defer c.now.Add(-1)
	for {
		m := c.max.Load()
		if n <= m || c.max.CompareAndSwap(m, n) {
			break
		}
	}
	if c.panicOn != "" && strings.Contains(t.URL, c.panicOn) {
		panic("проверка упала")
	}
	time.Sleep(30 * time.Millisecond)
	return probe.Result{Grade: probe.GradeGreen, Ratio: 2}
}

func (c *countingChecker) Light(_ context.Context, t probe.Target) probe.Result { return c.run(t) }
func (c *countingChecker) Full(_ context.Context, t probe.Target) probe.Result  { return c.run(t) }

// moduleWithStreams — модуль без Run: n живых источников в базе и в пуле.
func moduleWithStreams(t *testing.T, n int, ch *countingChecker) (*Module, []int64) {
	t.Helper()
	d := openDB(t)
	pl := &Playlist{Name: "a", AddedAt: time.Now()}
	if err := d.insertPlaylist(context.Background(), pl); err != nil {
		t.Fatal(err)
	}
	var es []m3u.Entry
	for i := range n {
		es = append(es, m3u.Entry{Name: fmt.Sprintf("Канал %d", i), URL: fmt.Sprintf("http://s/%d.m3u8", i)})
	}
	if _, _, err := d.replaceEntries(context.Background(), pl.ID, es); err != nil {
		t.Fatal(err)
	}
	m := New(Options{DB: d.DB, Dir: t.TempDir(), Prober: ch})
	if err := m.reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for id := range m.pool.streams {
		ids = append(ids, id)
	}
	return m, ids
}

// Полная проверка — не больше 8 источников одновременно на весь модуль, сколько бы проходов и кнопок
// «Проверить» ни шло сразу (спека этапа 8, раздел 5.8, критерий 7).
func TestFullLimitIsShared(t *testing.T) {
	ch := &countingChecker{}
	m, ids := moduleWithStreams(t, 30, ch)
	var wg sync.WaitGroup
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.check(context.Background(), ids, "full", fullParallel, nil)
		}()
	}
	wg.Wait()
	if got := ch.max.Load(); got > fullParallel {
		t.Errorf("одновременно %d полных проверок, предел %d", got, fullParallel)
	}
	if ch.calls.Load() != 90 {
		t.Errorf("проверок %d, нужно 90", ch.calls.Load())
	}
}

// Паника в проверке одного источника не роняет процесс: остальные проверяются.
func TestCheckPanicDoesNotCrash(t *testing.T) {
	ch := &countingChecker{panicOn: "/3.m3u8"}
	m, ids := moduleWithStreams(t, 6, ch)
	m.check(context.Background(), ids, "light", lightParallel, nil)
	alive := 0
	for _, s := range m.pool.streams {
		if s.State == StateAlive {
			alive++
		}
	}
	if alive != 5 {
		t.Errorf("проверено %d из 5 (без упавшего)", alive)
	}
}

// Молчащий источник видимого канала — в полной проверке (иначе канал пропадает до 4:00); мёртвый —
// нет; канал скрытой категории — только если он у кого-то в избранном.
func TestFullTargetsIncludeSilent(t *testing.T) {
	ch := &countingChecker{}
	m, _ := moduleWithStreams(t, 0, ch)
	ix, base := testIndex(t)
	p := testPool(src{name: "НТВ", state: StateSilent}, src{name: "Матч ТВ", state: StateDead}, src{name: "BBC News", state: StateSilent},
		src{name: "Первый канал"})
	m.mu.Lock()
	m.pool, m.epg, m.base, m.hidden = p, ix, base, Hidden{Categories: []string{"news"}}
	m.mu.Unlock()
	m.rebuild(context.Background())
	got := func(favs map[string]bool) string {
		var out []string
		for _, id := range m.fullTargets(false, favs) {
			out = append(out, p.streams[id].Entries[0].Name)
		}
		return strings.Join(sortedCopy(out), ",")
	}
	if g := got(nil); g != "НТВ,Первый канал" {
		t.Errorf("к полной проверке: %s", g)
	}
	if g := got(map[string]bool{"bbc": true}); g != "BBC News,НТВ,Первый канал" {
		t.Errorf("с избранным из скрытой категории: %s", g)
	}
}

func sortedCopy(s []string) []string {
	out := append([]string(nil), s...)
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

// Сняли «ограничено» — новые источники плейлиста проверяются сразу, а не в 4:00.
func TestUnlimitingPlaylistChecksNow(t *testing.T) {
	f := newFakeNet(t)
	m, _ := startModule(t, f)
	limited := f.srv.URL + "/s/limited.m3u8"
	res, err := m.AddPlaylist(context.Background(), PlaylistInput{Name: "Платный", Limited: true,
		Data: []byte("#EXTM3U\n#EXTINF:-1,Спас\n" + limited + "\n")})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if f.limited.Load() != 0 {
		t.Fatal("ограниченный проверялся в фоне")
	}
	off := false
	if _, err := m.UpdatePlaylist(context.Background(), res.ID, PlaylistPatch{Limited: &off}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "проверка после снятия «ограничено»", func() bool { return f.limited.Load() > 0 })
}
