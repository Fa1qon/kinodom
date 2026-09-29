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

// Место по чтению потока (спека этапа 8, раздел 7.2). VLC читает поток короткими запросами (новое
// соединение каждые пару секунд — проверено вживую), поэтому место считается по сеансу: запросы одного
// устройства к одному файлу с перерывами меньше watchGap. Место — докуда дочитал самый свежий запрос;
// сеанс короче watchMin места не сообщает (плеер только открыл файл); прыжок в самый конец файла,
// который продержался меньше watchMin, не место (плеер читает индекс в конце при открытии).
// Переменные — тесты их ускоряют.
var (
	watchEvery = 10 * time.Second
	watchMin   = 10 * time.Second
	watchGap   = 30 * time.Second
)

// streamLead — насколько плеер читает поток впереди картинки: VLC 3 — около 3,3 МБ (вживую: сервер
// видел 114,8 с при картинке на 101-й секунде у AVI 1,9 Мбит/с). Место — с этой поправкой: лучше
// повторить несколько секунд, чем пропустить.
var streamLead int64 = 4 << 20

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

type watchKey struct {
	device string
	ih     metainfo.Hash
	index  int
}

// watchSession — сеанс просмотра файла на устройстве.
type watchSession struct {
	size     int64
	started  time.Time
	seen     time.Time // последний запрос начался или закончился
	active   int       // запросов идёт
	latest   *trackedReader
	latestAt time.Time
	pos      int64 // принятое место
	reported time.Time
}

// watchBegin — запрос потока начался; возвращает «запрос закончился».
func (s *Service) watchBegin(key watchKey, size int64, tr *trackedReader) func() {
	now := s.now()
	s.watchMu.Lock()
	defer s.watchMu.Unlock()
	if s.watchSessions == nil {
		s.watchSessions = map[watchKey]*watchSession{}
	}
	ss := s.watchSessions[key]
	if ss == nil {
		ss = &watchSession{size: size, started: now}
		s.watchSessions[key] = ss
	}
	ss.active++
	ss.latest, ss.latestAt, ss.seen = tr, now, now
	return func() {
		s.watchMu.Lock()
		defer s.watchMu.Unlock()
		ss.active--
		ss.seen = s.now()
	}
}

type watchReport struct {
	key    watchKey
	offset int64
	size   int64
}

// watchTick — раз в секунду из Run: принять место, сообщить его раз в watchEvery и в конце сеанса.
func (s *Service) watchTick(now time.Time) {
	if s.watch == nil {
		return
	}
	var out []watchReport
	s.watchMu.Lock()
	for key, ss := range s.watchSessions {
		if ss.latest != nil {
			p := ss.latest.pos.Load()
			jump := p >= ss.size*98/100 && ss.pos < ss.size*90/100 && now.Sub(ss.latestAt) < watchMin
			if !jump {
				ss.pos = p
			}
		}
		idle := ss.active == 0 && now.Sub(ss.seen) > watchGap
		if now.Sub(ss.started) >= watchMin && (now.Sub(ss.reported) >= watchEvery || idle) {
			out = append(out, watchReport{key, max(ss.pos-streamLead, 0), ss.size})
			ss.reported = now
		}
		if idle {
			delete(s.watchSessions, key)
		}
	}
	s.watchMu.Unlock()
	for _, r := range out {
		s.watch.Report(context.Background(), r.key.device, r.key.ih.HexString(), r.key.index, r.offset, r.size)
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
