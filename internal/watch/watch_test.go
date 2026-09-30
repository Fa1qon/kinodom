package watch

import (
	"context"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"
)

type fakeReporter struct {
	mu      sync.Mutex
	reports []string // «устройство раздача номер смещение/размер»
}

func (f *fakeReporter) Report(_ context.Context, device, hash string, index int, offset, size int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reports = append(f.reports, fmt.Sprintf("%s %s %d %d/%d", device, hash, index, offset, size))
}

func (f *fakeReporter) all() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.reports...)
}

// zeros — файл из нулей заданного размера без памяти под него.
type zeros struct{ size, off int64 }

func (z *zeros) Read(p []byte) (int, error) {
	if z.off >= z.size {
		return 0, io.EOF
	}
	n := int(min(int64(len(p)), z.size-z.off))
	clear(p[:n])
	z.off += int64(n)
	return n, nil
}

func (z *zeros) Seek(off int64, whence int) (int64, error) {
	switch whence {
	case io.SeekCurrent:
		off += z.off
	case io.SeekEnd:
		off += z.size
	}
	z.off = off
	return off, nil
}

// clock — ручные часы трекера.
type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

// request — один запрос плеера: с from прочитать n байт.
func request(tr *Tracker, k Key, size, from, n int64) {
	rs, done := tr.Wrap(k, size, &zeros{size: size})
	rs.Seek(from, io.SeekStart)
	io.CopyN(io.Discard, rs, n)
	done()
}

// Место — с поправкой на то, что плеер читает впереди картинки (Lead); не меньше нуля.
func TestLead(t *testing.T) {
	fr := &fakeReporter{}
	c := &clock{time.Unix(1000, 0)}
	tr := New(fr, c.now)
	for i, pos := range []int64{10 << 20, 1 << 20} {
		k := Key{"pc", "h", i}
		request(tr, k, 100<<20, pos, 0)
	}
	c.t = c.t.Add(Min)
	tr.Tick(c.t)
	r := fr.all()
	want := map[string]bool{fmt.Sprintf("pc h 0 %d/%d", 6<<20, 100<<20): true, fmt.Sprintf("pc h 1 0/%d", 100<<20): true}
	if len(r) != 2 || !want[r[0]] || !want[r[1]] {
		t.Errorf("с поправкой: %v", r)
	}
}

// Как VLC: короткие запросы с паузами меньше Gap — один сеанс; сеанс короче Min места не сообщает;
// в конце сеанса — итоговое место.
func TestShortRequestsOneSession(t *testing.T) {
	fr := &fakeReporter{}
	c := &clock{time.Unix(1000, 0)}
	tr := New(fr, c.now)
	k := Key{"tv", "lib-3", 7}
	for i := int64(0); i < 5; i++ {
		request(tr, k, 1<<30, i*(10<<20), 10<<20)
		c.t = c.t.Add(time.Second)
		tr.Tick(c.t)
	}
	if r := fr.all(); len(r) != 0 {
		t.Fatalf("сеанс 5 с — рано сообщать: %v", r)
	}
	c.t = c.t.Add(Min)
	tr.Tick(c.t)
	c.t = c.t.Add(Gap + time.Second)
	tr.Tick(c.t)
	r := fr.all()
	last := fmt.Sprintf("tv lib-3 7 %d/%d", 50<<20-Lead, 1<<30)
	if len(r) == 0 || r[len(r)-1] != last {
		t.Errorf("итоговое место %v, нужно …%s", r, last)
	}
	n := len(r)
	c.t = c.t.Add(time.Hour)
	tr.Tick(c.t)
	if len(fr.all()) != n {
		t.Errorf("кончившийся сеанс сообщает снова: %v", fr.all())
	}
}

// Плеер читает индекс в конце файла (moov, Cues) — это не место, пока оттуда не прочитано JumpRead.
func TestTailIndexNotPosition(t *testing.T) {
	fr := &fakeReporter{}
	c := &clock{time.Unix(1000, 0)}
	tr := New(fr, c.now)
	k := Key{"pc", "h", 0}
	size := int64(300 << 20)
	request(tr, k, size, 0, 20<<20)
	c.t = c.t.Add(time.Second) // тик раз в секунду, как в Run владельца
	tr.Tick(c.t)
	request(tr, k, size, size-4096, 4096)
	c.t = c.t.Add(Min)
	tr.Tick(c.t)
	if r := fr.all(); len(r) != 1 || r[0] != fmt.Sprintf("pc h 0 %d/%d", 20<<20-Lead, size) {
		t.Errorf("индекс в конце стал местом: %v", r)
	}
}

// Без истории трекер ничего не считает и не мешает потоку.
func TestNilReporter(t *testing.T) {
	tr := New(nil, nil)
	z := &zeros{size: 10}
	rs, done := tr.Wrap(Key{}, 10, z)
	if rs != io.ReadSeeker(z) {
		t.Errorf("без истории читатель должен остаться тем же")
	}
	done()
	tr.Tick(time.Now())
	var nilTracker *Tracker
	rs, done = nilTracker.Wrap(Key{}, 10, z)
	done()
	nilTracker.Tick(time.Now())
	if rs != io.ReadSeeker(z) {
		t.Errorf("nil-трекер")
	}
}
