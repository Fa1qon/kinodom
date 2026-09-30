package torrents

import (
	"context"
	"io"
	"sync"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"

	"kinodom/internal/media"
	"kinodom/internal/watch"
)

// WatchTracker — история просмотров (спека этапа 8, раздел 7): куда поток сообщает место и откуда
// «Смотреть» берёт место продолжения. Реализует history.Service.
type WatchTracker interface {
	Report(ctx context.Context, device, hash string, index int, offset, size int64)
	Duration(ctx context.Context, hash string, index int) (float64, bool)
	SetDuration(ctx context.Context, hash string, index int, sec float64) error
	StartSec(ctx context.Context, device, hash string, index int) int
}

// SetWatchTracker подключает историю просмотров; nil — без неё. Вызывать до Run. Место по чтению
// потока считает общий пакет watch (спека этапа 8, раздел 7.2).
func (s *Service) SetWatchTracker(t WatchTracker) {
	s.watch = t
	if t != nil {
		s.tracker = watch.New(t, func() time.Time { return s.now() })
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
