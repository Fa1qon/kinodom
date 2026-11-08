package torrents

import (
	"context"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"

	"kinodom/internal/httpx"
	"kinodom/internal/watch"
)

// StreamHandler отдаёт файл раздачи с поддержкой Range: GET /stream/{hash}/{index}/{name}.
// Перемотка — это новый Range-запрос; движок сам качает нужное место первым.

// streamAheadSeconds — на сколько секунд показа впереди позиции читать при потоке. Большое
// окно нужно телевизору: при кратком падении скорости он продолжает читать уже скачанное,
// как TorServer/LAMPA. Верхняя граница по байтам не даёт одному потоку занять весь канал.
const streamAheadSeconds = 600
const streamAheadMaxBytes = 256 << 20
const streamAheadMinBytes = 32 << 20

func streamAheadBytes(bitrate float64) int64 {
	if bitrate <= 0 {
		return streamAheadMinBytes
	}
	n := int64(bitrate * streamAheadSeconds)
	if n < streamAheadMinBytes {
		return streamAheadMinBytes
	}
	if n > streamAheadMaxBytes {
		return streamAheadMaxBytes
	}
	return n
}

// streamReadaheadBytes — окно Reader потока (ступень Readahead): 16 МБ, как AdjustRA у TorrServer, но не
// меньше четырёх кусков. Дальше окна куски получают High из streamWindow.
func streamReadaheadBytes(pieceLen int64) int64 {
	return max(16<<20, 4*pieceLen)
}

func (s *Service) StreamHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ih, ok := parseHash(w, r)
		if !ok {
			return
		}
		t, found := s.eng.cl.Torrent(ih)
		if !found {
			httpx.WriteError(w, http.StatusNotFound, ErrNotOpen.Error())
			return
		}
		select {
		case <-t.GotInfo():
		case <-r.Context().Done():
			return
		}
		index, err := strconv.Atoi(r.PathValue("index"))
		files := t.Files()
		if err != nil || index < 0 || index >= len(files) {
			httpx.WriteError(w, http.StatusNotFound, ErrNoSuchFile.Error())
			return
		}
		f := files[index]
		if !s.isStored(ih, index) {
			// Старая ссылка телевизора на удалённый файл не должна докачивать его снова (ревью этапа 6).
			httpx.WriteError(w, http.StatusGone, "Файл удалён — откройте раздачу заново")
			return
		}
		s.activeStreams.Add(1)
		defer s.activeStreams.Add(-1)
		release, progress := s.openPlaybackReader(ih, index)
		defer release()
		defer s.keeper.Acquire()() // ПК не засыпает, пока смотрят (спека, раздел 9)
		s.touchStream(r.Context(), ih, index)

		// Свой читатель на каждый запрос: у каждого телевизора своя позиция.
		rd := f.NewReader()
		rd.SetContext(r.Context()) // зритель ушёл — чтение прерывается и не держит приоритеты кусков
		rd.SetResponsive()         // отдавать данные, не дожидаясь проверки целого куска — быстрая перемотка
		rd.SetReadahead(streamReadaheadBytes(t.Info().PieceLength))
		var bodyClose io.Closer = rd
		var body io.ReadSeeker = rd
		// Уже проверенное непрерывное начало лежит на диске быстрее, чем его повторное
		// чтение через torrent.Reader. Ограничиваем размер, чтобы обычный GET не заливал
		// клиент сотнями мегабайт до первого кадра.
		prefix := contiguousPrefix(t, f)
		maxPrefix := streamAheadBytes(s.bitrateOf(ih, index, f.Length()))
		prefix = min(prefix, maxPrefix)
		if prefix > 0 {
			path := enginePath(s.eng.TorrentDir(ih), t.Info(), ih, f.FileInfo())
			if disk, err := os.Open(path); err == nil {
				body = &prefixReader{disk: disk, tail: rd, prefix: prefix, length: f.Length()}
				bodyClose = body.(io.Closer)
			}
		}
		defer bodyClose.Close()
		// Buffer the source, but keep the history tracker outside it so short
		// Range requests report only bytes actually sent to the player.
		buffered := io.ReadSeeker(newBufferedReadSeeker(body, 1<<20))

		ct := videoTypes[extOf(f.DisplayPath())]
		if ct == "" {
			ct = "application/octet-stream"
		}
		w.Header().Set("Content-Type", ct) // иначе ServeContent стал бы угадывать тип по байтам
		if s.watch != nil {                // место по чтению — в историю устройства (спека этапа 8, раздел 7.2)
			s.learnDuration(ih, index, f)
			if !httpx.OwnPlayer(r) { // свой плеер сообщает место сам (спека цикла 18, раздел 4)
				var done func()
				body, done = s.tracker.Wrap(watch.Key{Device: httpx.Device(r), Hash: ih.HexString(), Index: index}, f.Length(), buffered)
				defer done()
			} else {
				body = buffered
			}
		} else {
			body = buffered
		}
		// ServeContent делает Seek перед первым Read и плееры часто присылают
		// новые Range-запросы. Отслеживаем фактическую позицию, чтобы на границе
		// непрерывного буфера поднять только ближайшее окно этого зрителя.
		body = &progressReadSeeker{ReadSeeker: body, onPosition: progress}
		http.ServeContent(w, r, "", time.Time{}, body)
	})
}

type progressReadSeeker struct {
	io.ReadSeeker
	onPosition func(int64)
	pos        int64
}

func (r *progressReadSeeker) Read(p []byte) (int, error) {
	n, err := r.ReadSeeker.Read(p)
	if n > 0 && r.onPosition != nil {
		r.pos += int64(n)
		r.onPosition(r.pos)
	}
	return n, err
}

func (r *progressReadSeeker) Seek(offset int64, whence int) (int64, error) {
	pos, err := r.ReadSeeker.Seek(offset, whence)
	if err == nil && r.onPosition != nil {
		r.pos = pos
		r.onPosition(pos)
	}
	return pos, err
}

// contiguousPrefix — длина проверенного участка файла с начала. BytesCompleted
// считает и куски в середине, поэтому для безопасного чтения разрежённого файла
// нужны именно последовательные Complete-куски.
func contiguousPrefix(t *torrent.Torrent, f *torrent.File) int64 {
	info := t.Info()
	if info == nil {
		return 0
	}
	overlap := func(i int) int64 {
		p := info.Piece(i)
		return max(0, min(p.Offset()+p.Length(), f.Offset()+f.Length())-max(p.Offset(), f.Offset()))
	}
	var got int64
	for i := f.BeginPieceIndex(); i < f.EndPieceIndex() && t.PieceState(i).Complete; i++ {
		got += overlap(i)
	}
	return min(got, f.Length())
}

// ActiveStreams — сколько потоков идёт прямо сейчас (запрет сна и урезание отдачи — этап 6).
func (s *Service) ActiveStreams() int { return int(s.activeStreams.Load()) }

func (s *Service) bitrateOf(ih metainfo.Hash, index int, size int64) float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ss, ok := s.sessions[ih]; ok {
		if p, ok := ss.prepared[index]; ok {
			return p.bitrate
		}
	}
	return estimateBitrate(size, false)
}

// parseHash читает {hash} из пути; при ошибке сам отвечает 400.
func parseHash(w http.ResponseWriter, r *http.Request) (metainfo.Hash, bool) {
	var ih metainfo.Hash
	if err := ih.FromHexString(r.PathValue("hash")); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "неверный идентификатор раздачи")
		return ih, false
	}
	return ih, true
}

// isStored — файл хранится: поток отдаётся только по таким файлам.
func (s *Service) isStored(ih metainfo.Hash, index int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	ss, ok := s.sessions[ih]
	return ok && ss.storedFiles[index]
}

// touchInterval — «сейчас смотрят» пишется в базу не чаще раза в минуту на файл: Range-запросов у
// плеера — десятки в минуту, а пишущее соединение с базой одно (хвост этапа 2).
const touchInterval = time.Minute

// touchStream отмечает просмотр файла в базе, но не чаще touchInterval.
func (s *Service) touchStream(ctx context.Context, ih metainfo.Hash, index int) {
	now := s.now()
	s.mu.Lock()
	ss := s.sessions[ih]
	if ss == nil || now.Sub(ss.touched[index]) < touchInterval {
		s.mu.Unlock()
		return
	}
	ss.touched[index] = now
	s.mu.Unlock()
	if err := s.reg.TouchStream(ctx, ih, index, now); err != nil {
		s.log.Warn("не удалось отметить просмотр", "err", err)
	}
}

// openReader отмечает открытый поток к файлу (такой файл «сейчас смотрят», не удаляется и
// докачивается вне очереди); возвращает отметку закрытия.
func (s *Service) openReader(ih metainfo.Hash, index int) func() {
	release, _ := s.openPlaybackReader(ih, index)
	return release
}

func (s *Service) openPlaybackReader(ih metainfo.Hash, index int) (func(), func(int64)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ss := s.sessions[ih]
	if ss == nil {
		return func() {}, func(int64) {}
	}
	ss.nextReaderID++
	id := ss.nextReaderID
	ss.readerPos[id] = readerPosition{index: index}
	if ss.readers[index]++; ss.readers[index] == 1 {
		s.applyLocked(ss) // первый зритель — файл качается, даже если он не в фокусе очереди
	}
	release := func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		delete(ss.readerPos, id)
		if ss.readers[index]--; ss.readers[index] == 0 {
			ss.held[index] = false
			s.applyLocked(ss)
		}
	}
	progress := func(pos int64) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if rp, ok := ss.readerPos[id]; ok {
			rp.pos = max(0, pos)
			ss.readerPos[id] = rp
		}
	}
	return release, progress
}

// streaming — к какому-то файлу раздачи открыт поток. Вызывать под s.mu.
func (ss *session) streaming() bool {
	for _, n := range ss.readers {
		if n > 0 {
			return true
		}
	}
	return false
}
