package torrents

import (
	"context"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"

	"kinodom/internal/media"
)

// WatchTracker — история просмотров (спека этапа 8, раздел 7): куда поток сообщает место и откуда
// «Смотреть» берёт место продолжения. Реализует history.Service.
type WatchTracker interface {
	Report(ctx context.Context, device, hash string, index int, offset, size int64)
	Duration(ctx context.Context, hash string, index int) (float64, bool)
	SetDuration(ctx context.Context, hash string, index int, sec float64) error
	StartSec(ctx context.Context, device, hash string, index int) int
}

// Место по чтению потока: запрос, который идёт дольше watchMin, раз в watchEvery и в конце сообщает,
// докуда дочитан файл. Короткие запросы (индекс в конце файла при открытии) не считаются.
// Переменные — тесты их ускоряют.
var (
	watchEvery = 10 * time.Second
	watchMin   = 10 * time.Second
)

// SetWatchTracker подключает историю просмотров; nil — без неё. Вызывать до Run.
func (s *Service) SetWatchTracker(t WatchTracker) { s.watch = t }

// trackedReader — читатель файла, который помнит, докуда дочитали.
type trackedReader struct {
	rs  io.ReadSeeker
	pos atomic.Int64
}

func (t *trackedReader) Read(p []byte) (int, error) {
	n, err := t.rs.Read(p)
	t.pos.Add(int64(n))
	return n, err
}

func (t *trackedReader) Seek(off int64, whence int) (int64, error) {
	n, err := t.rs.Seek(off, whence)
	if err == nil {
		t.pos.Store(n)
	}
	return n, err
}

// trackWatch сообщает место, пока идёт запрос; возвращает «запрос закончился».
func (s *Service) trackWatch(device string, ih metainfo.Hash, index int, size int64, tr *trackedReader) func() {
	start := time.Now()
	done := make(chan struct{})
	hash := ih.HexString()
	report := func() {
		if time.Since(start) >= watchMin {
			s.watch.Report(context.Background(), device, hash, index, tr.pos.Load(), size)
		}
	}
	go func() {
		t := time.NewTicker(watchEvery)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				report()
			}
		}
	}()
	return func() {
		close(done)
		report()
	}
}

// learnDuration — длительность файла из заголовка, один раз за работу службы (7.2). Куски начала и
// конца файла качаются первыми (prepare), поэтому чтение заголовка не ждёт всего файла.
func (s *Service) learnDuration(ih metainfo.Hash, index int, f *torrent.File) {
	hash := ih.HexString()
	key := durationKey{hash, index}
	if _, loaded := s.durTried.LoadOrStore(key, true); loaded {
		return
	}
	if _, ok := s.watch.Duration(context.Background(), hash, index); ok {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		rd := f.NewReader()
		defer rd.Close()
		rd.SetContext(ctx)
		rd.SetResponsive()
		rd.SetReadahead(64 << 10)
		d, err := media.Duration(&readerAt{rs: rd}, f.Length(), extOf(f.DisplayPath()))
		if err != nil {
			if ctx.Err() != nil {
				s.durTried.Delete(key) // куски не успели прийти — попробуем при следующем просмотре
			}
			return
		}
		if err := s.watch.SetDuration(context.Background(), hash, index, d); err != nil {
			s.log.Warn("длительность файла не записалась", "err", err)
		}
	}()
}

type durationKey struct {
	hash  string
	index int
}

// readerAt — io.ReaderAt поверх читателя торрента.
type readerAt struct {
	mu sync.Mutex
	rs io.ReadSeeker
}

func (r *readerAt) ReadAt(p []byte, off int64) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, err := r.rs.Seek(off, io.SeekStart); err != nil {
		return 0, err
	}
	return io.ReadFull(r.rs, p)
}
