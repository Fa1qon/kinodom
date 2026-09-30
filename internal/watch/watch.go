// Package watch — где остановились, по чтению потока (спека этапа 8, раздел 7.2): общее для потока
// раздач и файлов медиатеки (спека этапа 9, раздел 5.6).
//
// VLC читает поток короткими запросами (новое соединение каждые пару секунд — проверено вживую),
// поэтому место считается по сеансу: запросы одного устройства к одному файлу с перерывами меньше Gap.
// Место — докуда дочитал самый свежий запрос; сеанс короче Min места не сообщает (плеер только открыл
// файл); прыжок в самый конец файла, откуда прочитано меньше JumpRead, не место (плеер читает индекс в
// конце при открытии).
package watch

import (
	"context"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

// Переменные — тесты их ускоряют.
var (
	Every = 10 * time.Second // как часто сообщать место идущего сеанса
	Min   = 10 * time.Second // сеанс короче — места нет
	Gap   = 30 * time.Second // перерыв между запросами, после которого сеанс кончился
)

// Lead — насколько плеер читает поток впереди картинки: VLC 3 — около 3,3 МБ (вживую: сервер видел
// 114,8 с при картинке на 101-й секунде у AVI 1,9 Мбит/с). Место — с этой поправкой: лучше повторить
// несколько секунд, чем пропустить.
var Lead int64 = 4 << 20

// JumpRead — сколько нужно прочитать с места в самом конце файла, чтобы это было место, а не индекс
// (moov, idx1, Cues), который плеер читает при открытии.
var JumpRead int64 = 8 << 20

// Key — файл на устройстве: «раздача» (infohash или lib-<единица>) и номер файла.
type Key struct {
	Device string
	Hash   string
	Index  int
}

// Reporter — история просмотров (history.Service).
type Reporter interface {
	Report(ctx context.Context, device, hash string, index int, offset, size int64)
}

// Tracker — сеансы просмотра. Нулевой указатель и трекер без Reporter ничего не считают.
type Tracker struct {
	r   Reporter
	now func() time.Time

	mu       sync.Mutex
	sessions map[Key]*session
}

// New — трекер; now nil — time.Now.
func New(r Reporter, now func() time.Time) *Tracker {
	if now == nil {
		now = time.Now
	}
	return &Tracker{r: r, now: now, sessions: map[Key]*session{}}
}

// reader — читатель файла, который помнит, докуда дочитали и сколько отдал после перемотки.
type reader struct {
	rs   io.ReadSeeker
	pos  atomic.Int64
	read atomic.Int64 // байт отдано после последней перемотки
}

func (t *reader) Read(p []byte) (int, error) {
	n, err := t.rs.Read(p)
	t.pos.Add(int64(n))
	t.read.Add(int64(n))
	return n, err
}

func (t *reader) Seek(off int64, whence int) (int64, error) {
	n, err := t.rs.Seek(off, whence)
	if err == nil {
		t.pos.Store(n)
		t.read.Store(0)
	}
	return n, err
}

type session struct {
	size     int64
	started  time.Time
	seen     time.Time // последний запрос начался или закончился
	active   int       // запросов идёт
	latest   *reader
	pos      int64 // принятое место
	reported time.Time
}

// Wrap — запрос потока начался: читатель, по которому считается место, и «запрос закончился».
func (t *Tracker) Wrap(k Key, size int64, rs io.ReadSeeker) (io.ReadSeeker, func()) {
	if t == nil || t.r == nil {
		return rs, func() {}
	}
	tr := &reader{rs: rs}
	now := t.now()
	t.mu.Lock()
	defer t.mu.Unlock()
	ss := t.sessions[k]
	if ss == nil {
		ss = &session{size: size, started: now}
		t.sessions[k] = ss
	}
	ss.active++
	ss.latest, ss.seen = tr, now
	return tr, func() {
		t.mu.Lock()
		defer t.mu.Unlock()
		ss.active--
		ss.seen = t.now()
	}
}

type report struct {
	k      Key
	offset int64
	size   int64
}

// Tick — раз в секунду из Run владельца: принять место, сообщить его раз в Every и в конце сеанса.
func (t *Tracker) Tick(now time.Time) {
	if t == nil || t.r == nil {
		return
	}
	var out []report
	t.mu.Lock()
	for k, ss := range t.sessions {
		if ss.latest != nil {
			p := ss.latest.pos.Load()
			jump := p >= ss.size*98/100 && ss.pos < ss.size*90/100 && ss.latest.read.Load() < JumpRead
			if !jump {
				ss.pos = p
			}
		}
		idle := ss.active == 0 && now.Sub(ss.seen) > Gap
		if now.Sub(ss.started) >= Min && (now.Sub(ss.reported) >= Every || idle) {
			out = append(out, report{k, max(ss.pos-Lead, 0), ss.size})
			ss.reported = now
		}
		if idle {
			delete(t.sessions, k)
		}
	}
	t.mu.Unlock()
	for _, r := range out {
		t.r.Report(context.Background(), r.k.Device, r.k.Hash, r.k.Index, r.offset, r.size)
	}
}
