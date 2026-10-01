package torrents

import (
	"bytes"
	"cmp"
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
	"kinodom/internal/watch"
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
	Hash  string         `json:"hash"`
	Name  string         `json:"name"`
	State TorrentState   `json:"state"`
	Peers int            `json:"peers"`
	Speed int64          `json:"speed"` // байт/с, полезные данные
	Focus int            `json:"focus"` // файл, который качается сейчас; −1 — никакой
	Files []FileProgress `json:"files"`
	Error string         `json:"error,omitempty"`
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
	upSpeed      float64 // отдача, байт/с, сглаженная
	lastUp       int64
	prepared     map[int]*prepared
	storedFiles  map[int]bool      // файлы, которые хранятся и докачиваются (в том числе до перезапуска)
	readers      map[int]int       // открытые потоки по файлам: такой файл «сейчас смотрят»
	touched      map[int]time.Time // когда просмотр файла последний раз записан в базу
	paused       map[int]bool      // докачка на паузе: мало места (этап 6)
	quiet        bool              // раздача молчит: не входит в раздаваемые (этап 6)
	verifyQ      map[int]bool      // куски, ждущие перепроверки по хэшу (и проверяемый сейчас)
	raw          []byte            // содержимое .torrent, пока метаинфо не сохранена
	metaSaved    bool              // метаинфо в базе
	lastSeen     time.Time         // когда раздачу последний раз открывали или спрашивали о ней
	focus        int               // файл, который качается сейчас (очередь загрузки, этап 7); −1 — никакой
	wantAll      bool              // «Скачать» до получения списка файлов: скачать всё, когда он придёт
	downloadErr  string            // отложенное «Скачать» не удалось (мало места)
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
	fetching map[metainfo.Hash]chan struct{} // идёт FetchInfo: временная раздача без хранилища

	expiredAt  time.Time     // когда последний раз чистили по сроку хранения (только Run)
	keeper     *power.Keeper // запрет сна, пока идёт поток; nil — без него
	upload     float64       // лимит отдачи, выставленный сейчас (только Run)
	uploadBase float64       // лимит отдачи из настроек, заданный на ходу (SetUploadLimit)
	uploadSet  bool
	uploadOff  bool                            // «не раздавать»: раздачи не отдают куски
	toVerify   []pieceRef                      // куски на перепроверку: файл отметок был повреждён
	verifyNow  pieceRef                        // кусок, который проверяется прямо сейчас (без s.mu)
	spaceMu    sync.Mutex                      // одна проверка места за раз (Prepare, уборка)
	freeSpace  func(dir string) (int64, error) // свободное место на диске папки; тесты подменяют
	totalSpace func(dir string) (int64, error) // размер диска папки

	activeStreams atomic.Int32
	watch         WatchTracker   // история просмотров; nil — без неё (этап 8c)
	durTried      sync.Map       // durationKey → длительность уже пробовали узнать
	tracker       *watch.Tracker // сеансы просмотра для истории; nil — без истории
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
		policy:       Policy{KeepFor: defaultKeepFor, MinFree: defaultMinFree, MaxSeeding: defaultMaxSeeding, KeepBehind: defaultKeepBehind},
		upload:       -1,
		freeSpace:    diskFree,
		totalSpace:   diskTotal,
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
			s.tracker.Tick(s.now())
			s.verifySome(500 * time.Millisecond)
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
	// Первый вызов — при запуске модуля: раздачи без папки (до этапа 6) лежат в текущей папке.
	if err := s.reg.PinDirs(ctx, s.eng.DownloadsDir()); err != nil {
		return err
	}
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
		ss.stored, ss.metaSaved = true, true
		// Хранимые файлы докачиваются дальше (и раздаются) по очереди, остальные не нужны.
		files := t.Files()
		for _, i := range idxs {
			if i >= 0 && i < len(files) {
				ss.storedFiles[i] = true
			}
		}
		ss.focus = rec.Focus
		if s.eng.recreated {
			s.queueVerify(ss)
		}
		if ss.focus < 0 || !ss.storedFiles[ss.focus] {
			s.setFocusLocked(ss, s.nextFocusLocked(ss))
		}
		s.applyLocked(ss)
		s.mu.Unlock()
	}
	// «Скачать» без списка файлов — раздача снова открывается и ждёт метаинфо от пиров.
	pending, err := s.reg.Pending(ctx)
	if err != nil {
		return err
	}
	for _, rec := range pending {
		if t, ok := s.eng.cl.Torrent(rec.InfoHash); ok {
			s.mu.Lock()
			s.sessionFor(t).wantAll = true // уже восстановлена с хранимыми файлами — применить остальное
			s.mu.Unlock()
			continue
		}
		s.eng.SetTorrentDir(rec.InfoHash, rec.Dir)
		var t *torrent.Torrent
		if rec.Metainfo != nil {
			if mi, lerr := metainfo.Load(bytes.NewReader(rec.Metainfo)); lerr == nil {
				t, err = s.eng.cl.AddTorrent(mi)
			}
		} else if strings.HasPrefix(rec.Source, "magnet:") {
			t, err = s.eng.cl.AddMagnet(rec.Source)
		}
		if t == nil || err != nil {
			s.log.Warn("отложенное «Скачать» не восстановилось", "hash", rec.InfoHash.HexString(), "err", err)
			continue
		}
		s.mu.Lock()
		ss := s.sessionFor(t)
		ss.wantAll = true
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
	if err := s.waitFetch(ctx, ih); err != nil {
		return metainfo.Hash{}, err
	}
	// Папку раздачи движок должен знать до добавления: файлы создаются сразу, как придёт метаинфо.
	dir, err := s.reg.Remember(ctx, ih, source, s.eng.DownloadsDir())
	if err != nil {
		return ih, err
	}
	if _, err := os.Stat(dir); err != nil && dir != s.eng.DownloadsDir() {
		// Раздача качалась на диск, которого сейчас нет: понятный текст вместо ошибки хранилища.
		return metainfo.Hash{}, fmt.Errorf("папка раздачи недоступна: %s — подключите диск", dir)
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
	if ss.raw == nil {
		ss.raw = raw
	}
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
		touched: map[int]time.Time{}, paused: map[int]bool{}, verifyQ: map[int]bool{}, lastSeen: s.now(), focus: -1}
	if s.uploadOff {
		t.DisallowDataUpload()
	}
	s.sessions[ih] = ss
	return ss
}

// saveMetainfo сохраняет метаинфо раздачи, которую движок уже получил: после перезапуска её не
// придётся ждать от пиров. Зовётся из цикла Run (хвост этапа 2: раньше — горутина на каждое
// открытие, висевшая, пока сеть не готова).
func (s *Service) saveMetainfo(ss *session) {
	s.mu.Lock() // raw пишет Open под s.mu — читать под ним же (гонка данных, хвост Х36)
	raw := ss.raw
	s.mu.Unlock()
	if raw == nil {
		b, err := bencode.Marshal(ss.t.Metainfo())
		if err != nil {
			s.log.Warn("метаинфо не сериализуется", "err", err)
			return
		}
		raw = b
	}
	if err := s.reg.SaveMetainfo(context.Background(), ss.t.InfoHash(), ss.t.Name(), raw); err != nil {
		s.log.Warn("метаинфо не сохранилась", "hash", ss.t.InfoHash().HexString(), "err", err)
		return
	}
	s.mu.Lock()
	ss.metaSaved, ss.raw = true, nil
	s.mu.Unlock()
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
		Speed: int64(ss.speed),
		Focus: ss.focus,
		Files: []FileProgress{},
		Error: cmp.Or(ss.errText, ss.downloadErr),
	}
	if ss.state == StateReady {
		st.Files = s.progressLocked(ss)
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
		if ss.wantAll {
			// Отложенное «Скачать» не вернётся после перезапуска: раздачу откроют снова кнопкой.
			ss.wantAll = false
			if err := s.reg.SetDownloadAll(context.Background(), ss.t.InfoHash(), false); err != nil {
				s.log.Warn("отложенное «Скачать» не снялось", "err", err)
			}
		}
	}
}

// sample раз в секунду обновляет сглаженную скорость и состояния всех раздач, переводит фокус
// загрузки на следующий файл и применяет отложенное «Скачать», когда пришёл список файлов.
func (s *Service) sample() {
	now := s.now()
	var unsaved []*session
	var want []metainfo.Hash
	advanced := false
	s.mu.Lock()
	for ih, ss := range s.sessions {
		st := ss.t.Stats()
		// Полезные байты: без повторов, отброшенных кусков и служебного (хвост этапа 2).
		b, up := st.BytesReadUsefulData.Int64(), st.BytesWrittenData.Int64()
		if !ss.lastSample.IsZero() {
			if dt := now.Sub(ss.lastSample).Seconds(); dt > 0 {
				ss.speed = 0.5*ss.speed + 0.5*float64(b-ss.lastBytes)/dt
				ss.upSpeed = 0.5*ss.upSpeed + 0.5*float64(up-ss.lastUp)/dt
			}
		}
		ss.lastBytes, ss.lastUp, ss.lastSample = b, up, now
		s.observe(ss, now)
		if !ss.metaSaved && ss.t.Info() != nil {
			unsaved = append(unsaved, ss)
		}
		if ss.wantAll && ss.t.Info() != nil {
			want = append(want, ih)
		}
		if s.advanceLocked(ss) {
			advanced = true
		}
	}
	s.mu.Unlock()
	for _, ss := range unsaved { // запись в базу — без s.mu
		s.saveMetainfo(ss)
	}
	// Очередь перешла к следующему файлу — влезет ли он: при нехватке места удаляются серии позади,
	// не помогло — пауза и «Мало места» (решение заказчика, этап 7a).
	if advanced {
		if err := s.checkSpace(context.Background()); err != nil {
			s.log.Warn("проверка места не удалась", "err", err)
		}
	}
	for _, ih := range want {
		if err := s.Download(context.Background(), ih, nil); err != nil {
			s.log.Warn("отложенное «Скачать» не удалось", "hash", ih.HexString(), "err", err)
			s.mu.Lock()
			if ss, ok := s.sessions[ih]; ok {
				ss.wantAll, ss.downloadErr = false, err.Error()
			}
			s.mu.Unlock()
			if err := s.reg.SetDownloadAll(context.Background(), ih, false); err != nil {
				s.log.Warn("отложенное «Скачать» не снялось", "err", err)
			}
		}
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

// KnownFiles — видеофайлы раздачи без открытия: из движка, если раздача открыта, иначе из
// сохранённой метаинфо. false — список ещё неизвестен (раздачу не открывали или метаданных нет).
func (s *Service) KnownFiles(ctx context.Context, ih metainfo.Hash) ([]FileInfo, bool, error) {
	s.mu.Lock()
	if ss, ok := s.sessions[ih]; ok && ss.t.Info() != nil {
		fs := playableFiles(allFiles(ss.t))
		s.mu.Unlock()
		return fs, true, nil
	}
	s.mu.Unlock()
	raw, ok, err := s.reg.Metainfo(ctx, ih)
	if err != nil || !ok {
		return nil, false, err
	}
	fs, err := PlayableFiles(raw)
	if err != nil {
		return nil, false, nil // битая метаинфо — как неизвестная: список придёт от пиров
	}
	return fs, true, nil
}

// fileName — путь файла внутри открытой раздачи; false — раздача не открыта, списка файлов ещё
// нет или номера нет.
func (s *Service) fileName(ih metainfo.Hash, index int) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ss, ok := s.sessions[ih]
	if !ok || ss.t.Info() == nil || index >= len(ss.t.Files()) {
		return "", false
	}
	return ss.t.Files()[index].DisplayPath(), true
}

// UseKeeper — запрет сна на время потоков (общий для всех модулей); вызывать до Run.
func (s *Service) UseKeeper(k *power.Keeper) { s.keeper = k }
