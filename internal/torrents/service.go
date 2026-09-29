package torrents

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"

	"kinodom/internal/power"
	"kinodom/internal/supervisor"
)

var (
	ErrNotOpen    = errors.New("раздача не открыта")
	ErrNoInfo     = errors.New("список файлов раздачи ещё не получен")
	ErrNoSuchFile = errors.New("такого файла в раздаче нет")
)

// Source — откуда открыть раздачу: magnet-ссылка или содержимое файла .torrent.
type Source struct {
	Magnet  string
	Torrent []byte
}

// TorrentStatus — состояние открытой раздачи для клиента.
type TorrentStatus struct {
	Hash  string       `json:"hash"`
	Name  string       `json:"name"`
	State TorrentState `json:"state"`
	Peers int          `json:"peers"`
	Files []FileInfo   `json:"files"`
	Error string       `json:"error,omitempty"`
}

// session — всё, что сервис знает об открытой раздаче.
type session struct {
	t            *torrent.Torrent
	stored       bool // есть хранимые файлы: правила «нет пиров» не применяются
	noPeersSince time.Time
	peersSince   time.Time
	state        TorrentState
	errText      string
	lastBytes    int64
	lastSample   time.Time
	speed        float64 // байт/с, сглаженная
	prepared     map[int]*prepared
	storedFiles  map[int]bool // файлы, которые хранятся и докачиваются (в том числе до перезапуска)
	readers      map[int]int  // открытые потоки по файлам: такой файл «сейчас смотрят»
	paused       map[int]bool // докачка на паузе: мало места (этап 6)
	quiet        bool         // раздача молчит: не входит в раздаваемые (этап 6)
	lastSeen     time.Time    // когда раздачу последний раз открывали или спрашивали о ней
}

// prepared — файл, выбранный для просмотра.
type prepared struct {
	bitrate    float64 // оценка, байт/с
	head, tail pieceSpan
}

// Service — модуль «torrents».
type Service struct {
	eng       *Engine                 // nil, пока движок не создан (NewLazyService)
	newEngine func() (*Engine, error) // создаёт движок в Run; ошибка — сбой модуля, сторож повторит
	onEngine  func(err error)         // сообщает, удалось ли создать движок (проблема в «Состоянии»)
	reg       *Registry
	log       *slog.Logger
	now       func() time.Time

	noPeersAfter, noMetaAfter time.Duration

	mu       sync.Mutex
	sessions map[metainfo.Hash]*session
	policy   Policy

	expiredAt time.Time                       // когда последний раз чистили по сроку хранения (только Run)
	keeper    *power.Keeper                   // запрет сна, пока идёт поток; nil — без него
	upload    float64                         // лимит отдачи, выставленный сейчас (только Run)
	spaceMu   sync.Mutex                      // одна проверка места за раз (Prepare, уборка)
	freeSpace func(dir string) (int64, error) // свободное место на диске папки; тесты подменяют

	activeStreams atomic.Int32
}

func NewService(eng *Engine, reg *Registry, log *slog.Logger) *Service {
	return &Service{
		eng:          eng,
		reg:          reg,
		log:          log,
		now:          time.Now,
		noPeersAfter: noPeersAfter,
		noMetaAfter:  noMetadataAfter,
		sessions:     map[metainfo.Hash]*session{},
		policy:       Policy{KeepFor: defaultKeepFor, MinFree: defaultMinFree, MaxSeeding: defaultMaxSeeding},
		upload:       -1,
		freeSpace:    diskFree,
	}
}

// NewLazyService — сервис, который создаёт движок сам, в Run. Если папка загрузок ещё
// недоступна (USB-диск не подключился), Run возвращает ошибку, сторож повторяет с паузами,
// а маршруты модуля до тех пор отвечают 503 — и торренты оживают без перезапуска службы.
func NewLazyService(newEngine func() (*Engine, error), reg *Registry, log *slog.Logger, onEngine func(err error)) *Service {
	s := NewService(nil, reg, log)
	s.newEngine = newEngine
	s.onEngine = onEngine
	return s
}

func (s *Service) Name() string { return "torrents" }

// Engine — движок или nil, если он ещё не создан.
func (s *Service) Engine() *Engine {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.eng
}

// Run создаёт движок (если его ещё нет), восстанавливает хранимые раздачи и раз в секунду
// обновляет скорость и состояния. Движок переживает перезапуски Run: идущие потоки не рвутся.
func (s *Service) Run(ctx context.Context) error {
	if s.Engine() == nil {
		e, err := s.newEngine()
		if s.onEngine != nil {
			s.onEngine(err)
		}
		if err != nil {
			return fmt.Errorf("торрент-движок: %w", err)
		}
		s.mu.Lock()
		s.eng = e
		s.mu.Unlock()
	}
	if err := s.restore(ctx); err != nil {
		return fmt.Errorf("восстановление раздач: %w", err)
	}
	supervisor.Ready(ctx) // хранимые раздачи на месте — маршруты модуля можно обслуживать
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	maint := time.NewTimer(0) // уборка — сразу после старта, дальше раз в 5 минут
	defer maint.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
			s.sample()
			s.shapeUpload()
		case <-maint.C:
			if err := s.maintain(ctx); err != nil {
				return fmt.Errorf("уборка: %w", err)
			}
			maint.Reset(maintainEvery)
		}
	}
}

// restore добавляет в движок раздачи с хранимыми файлами — из метаинфо в базе, без пиров.
// Раздачи, которые уже в движке, не трогает: вызывается и повторно — вернуть раздачи с диска,
// который был недоступен.
func (s *Service) restore(ctx context.Context) error {
	recs, err := s.reg.Restorable(ctx)
	if err != nil {
		return err
	}
	missing := map[string]int{} // недоступная папка → сколько раздач в ней ждут
	for _, rec := range recs {
		if _, ok := s.eng.cl.Torrent(rec.InfoHash); ok {
			continue
		}
		if rec.Dir != "" {
			if _, err := os.Stat(rec.Dir); err != nil {
				// Диск не подключён: записи не удаляются, раздача вернётся вместе с диском.
				missing[rec.Dir]++
				continue
			}
		}
		mi, err := metainfo.Load(bytes.NewReader(rec.Metainfo))
		if err != nil {
			s.log.Warn("метаинфо раздачи не читается, пропускаю", "hash", rec.InfoHash.HexString(), "err", err)
			continue
		}
		s.eng.SetTorrentDir(rec.InfoHash, rec.Dir)
		t, err := s.eng.cl.AddTorrent(mi)
		if err != nil {
			s.log.Warn("раздача не восстановилась", "hash", rec.InfoHash.HexString(), "err", err)
			continue
		}
		idxs, err := s.reg.StoredFiles(ctx, rec.InfoHash)
		if err != nil {
			return err
		}
		s.mu.Lock()
		ss := s.sessionFor(t)
		ss.stored = true
		// Хранимые файлы докачиваются дальше (и раздаются), остальные не нужны.
		files := t.Files()
		for _, i := range idxs {
			if i >= 0 && i < len(files) {
				ss.storedFiles[i] = true
				files[i].SetPriority(torrent.PiecePriorityNormal)
			}
		}
		s.mu.Unlock()
	}
	if len(missing) == 0 {
		s.reg.clearProblem(ctx, "torrents.dirs")
		return nil
	}
	var parts []string
	for dir, n := range missing {
		parts = append(parts, fmt.Sprintf("%s (раздач: %d)", dir, n))
	}
	slices.Sort(parts)
	s.reg.setProblem(ctx, "torrents.dirs", "Папка загрузок недоступна: "+strings.Join(parts, ", ")+
		" — эти раздачи не раздаются и не докачиваются, пока диск не вернётся")
	return nil
}

// Open добавляет раздачу в движок. Повторный вызов для той же раздачи (второй телевизор)
// возвращает её же; после ошибки «нет раздающих» — начинает заново.
func (s *Service) Open(ctx context.Context, src Source) (metainfo.Hash, error) {
	var (
		mi     *metainfo.MetaInfo
		ih     metainfo.Hash
		raw    []byte
		source string
	)
	switch {
	case len(src.Torrent) > 0:
		m, lerr := metainfo.Load(bytes.NewReader(src.Torrent))
		if lerr != nil {
			return metainfo.Hash{}, fmt.Errorf("файл .torrent не читается: %w", lerr)
		}
		mi, ih, raw, source = m, m.HashInfoBytes(), src.Torrent, "torrent-file"
	case src.Magnet != "":
		// Движок паникует на ссылке без infohash, а не возвращает ошибку — проверяем сами.
		m, perr := metainfo.ParseMagnetUri(src.Magnet)
		if perr != nil {
			return metainfo.Hash{}, fmt.Errorf("magnet-ссылка не читается: в ней нет infohash раздачи (%v)", perr)
		}
		if m.InfoHash == (metainfo.Hash{}) {
			return metainfo.Hash{}, errors.New("magnet-ссылка не читается: infohash раздачи — одни нули")
		}
		ih, source = m.InfoHash, src.Magnet
	default:
		return metainfo.Hash{}, errors.New("не указан источник раздачи: нужна magnet-ссылка или .torrent")
	}
	// Под s.mu: уборка не должна убрать запись о раздаче между Remember и появлением сессии.
	s.mu.Lock()
	defer s.mu.Unlock()
	// Папку раздачи движок должен знать до добавления: файлы создаются сразу, как придёт метаинфо.
	dir, err := s.reg.Remember(ctx, ih, source, s.eng.DownloadsDir())
	if err != nil {
		return ih, err
	}
	s.eng.SetTorrentDir(ih, dir)
	var t *torrent.Torrent
	if mi != nil {
		if t, err = s.eng.cl.AddTorrent(mi); err != nil {
			return metainfo.Hash{}, fmt.Errorf("файл .torrent не принят: %w", err)
		}
	} else if t, err = s.eng.cl.AddMagnet(src.Magnet); err != nil {
		return metainfo.Hash{}, fmt.Errorf("magnet-ссылка не читается: %w", err)
	}
	ss := s.sessionFor(t)
	ss.lastSeen = s.now()
	s.observe(ss, s.now())
	go s.saveMetainfoWhenReady(t, raw)
	return ih, nil
}

// sessionFor — сессия раздачи; новая, если раздачу добавили заново (после ошибки).
// Вызывать под s.mu.
func (s *Service) sessionFor(t *torrent.Torrent) *session {
	ih := t.InfoHash()
	if ss, ok := s.sessions[ih]; ok && ss.t == t {
		return ss
	}
	ss := &session{t: t, prepared: map[int]*prepared{}, storedFiles: map[int]bool{}, readers: map[int]int{},
		paused: map[int]bool{}, lastSeen: s.now()}
	s.sessions[ih] = ss
	return ss
}

// saveMetainfoWhenReady сохраняет метаинфо, как только движок её получил.
func (s *Service) saveMetainfoWhenReady(t *torrent.Torrent, raw []byte) {
	select {
	case <-t.GotInfo():
	case <-t.Closed():
		return
	}
	if raw == nil {
		b, err := bencode.Marshal(t.Metainfo())
		if err != nil {
			s.log.Warn("метаинфо не сериализуется", "err", err)
			return
		}
		raw = b
	}
	if err := s.reg.SaveMetainfo(context.Background(), t.InfoHash(), t.Name(), raw); err != nil {
		s.log.Warn("метаинфо не сохранилась", "hash", t.InfoHash().HexString(), "err", err)
	}
}

// Status — состояние раздачи; false, если её не открывали.
func (s *Service) Status(ih metainfo.Hash) (TorrentStatus, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ss, ok := s.sessions[ih]
	if !ok {
		return TorrentStatus{}, false
	}
	ss.lastSeen = s.now()
	s.observe(ss, s.now())
	st := TorrentStatus{
		Hash:  ih.HexString(),
		Name:  ss.t.Name(),
		State: ss.state,
		Peers: ss.t.Stats().ActivePeers,
		Files: []FileInfo{},
		Error: ss.errText,
	}
	if ss.state == StateReady {
		st.Files = playableFiles(allFiles(ss.t))
	}
	return st, true
}

// observe применяет правила открытия к текущим наблюдениям. Вызывать под s.mu.
func (s *Service) observe(ss *session, now time.Time) {
	if ss.state == StateError {
		return
	}
	st := ss.t.Stats()
	active := st.ActivePeers
	if active > 0 {
		ss.noPeersSince = time.Time{}
		if ss.peersSince.IsZero() {
			ss.peersSince = now
		}
	} else if ss.noPeersSince.IsZero() && (s.eng.NetworkReady() || st.TotalPeers > 0) {
		// Сеть готова (DHT нашёл узлы или трекер вернул адреса) — пошёл отсчёт 30 с.
		ss.noPeersSince = now
	}
	ss.state, ss.errText = evalOpenState(openObs{
		now:          now,
		haveInfo:     ss.t.Info() != nil,
		activePeers:  active,
		noPeersSince: ss.noPeersSince,
		peersSince:   ss.peersSince,
		stored:       ss.stored,
	}, s.noPeersAfter, s.noMetaAfter)
	if ss.state == StateError {
		ss.t.Drop() // без хранимых файлов раздача больше не нужна
	}
}

// sample раз в секунду обновляет сглаженную скорость и состояния всех раздач.
func (s *Service) sample() {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ss := range s.sessions {
		st := ss.t.Stats()
		b := st.BytesReadData.Int64()
		if !ss.lastSample.IsZero() {
			if dt := now.Sub(ss.lastSample).Seconds(); dt > 0 {
				ss.speed = 0.5*ss.speed + 0.5*float64(b-ss.lastBytes)/dt
			}
		}
		ss.lastBytes, ss.lastSample = b, now
		s.observe(ss, now)
	}
}

// allFiles — все файлы раздачи (нужна метаинфо).
func allFiles(t *torrent.Torrent) []FileInfo {
	fs := t.Files()
	out := make([]FileInfo, len(fs))
	for i, f := range fs {
		out[i] = FileInfo{Index: i, Name: f.DisplayPath(), Size: f.Length()}
	}
	return out
}

// UseKeeper — запрет сна на время потоков (общий для всех модулей); вызывать до Run.
func (s *Service) UseKeeper(k *power.Keeper) { s.keeper = k }
