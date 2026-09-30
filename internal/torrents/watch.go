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
		s.tracker = watch.New(diskLead{WatchTracker: t, complete: s.fileComplete}, func() time.Time { return s.now() })
	}
}

// diskLead — место в истории у скачанного целиком файла (хвост Х15): VLC читает его с диска впереди на
// весь буфер, как файл медиатеки, — та же поправка (watch.ExtraLeadFor). У недокачанного — без неё.
type diskLead struct {
	WatchTracker
	complete func(hash string, index int) bool
}

func (d diskLead) Report(ctx context.Context, device, hash string, index int, offset, size int64) {
	if d.complete(hash, index) {
		offset = max(offset-watch.ExtraLeadFor(size), 0)
	}
	d.WatchTracker.Report(ctx, device, hash, index, offset, size)
}

// fileComplete — файл раздачи скачан целиком.
func (s *Service) fileComplete(hash string, index int) bool {
	var ih metainfo.Hash
	if err := ih.FromHexString(hash); err != nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ss := s.sessions[ih]
	if ss == nil || ss.t.Info() == nil || index < 0 || index >= len(ss.t.Files()) {
		return false
	}
	return fileDone(ss.t.Files()[index])
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
