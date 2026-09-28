package torrents

import (
	"context"
	"fmt"
	"net/url"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
)

// FileState — готовность файла к просмотру.
type FileState string

const (
	FileBuffering FileState = "buffering"
	FileReady     FileState = "ready"
	FileError     FileState = "error" // например, «мало места» (этап 6)
)

// FileStatus — то, что клиент показывает во время буферизации:
// «Буферизация 40 % · 12 пиров · 3 МБ/с» и «Без остановок через ~12 мин».
type FileStatus struct {
	State         FileState `json:"state"`
	BufferPercent int       `json:"bufferPercent"`
	Peers         int       `json:"peers"`
	Speed         int64     `json:"speed"` // байт/с
	SmoothInSec   int       `json:"smoothInSec"` // −1 — скорость нулевая, оценить нельзя
	StreamPath    string    `json:"streamPath"` // адрес сервера подставляет API
	Error         string    `json:"error,omitempty"`
}

// Prepare выбирает файл для просмотра: он качается целиком и хранится, а начало и конец —
// в первую очередь. Повторный вызов (второй телевизор) ничего не меняет.
//
// Всё делается под s.mu: иначе два телевизора, готовящие разные серии одновременно, могли бы
// сбросить друг другу докачку. Порядок блокировок тот же, что везде: s.mu, затем движок.
func (s *Service) Prepare(ctx context.Context, ih metainfo.Hash, index int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ss, ok := s.sessions[ih]
	if !ok {
		return ErrNotOpen
	}
	t := ss.t
	info := t.Info()
	if info == nil {
		return ErrNoInfo
	}
	files := t.Files()
	if index < 0 || index >= len(files) {
		return ErrNoSuchFile
	}
	if _, done := ss.prepared[index]; done {
		return nil
	}
	f := files[index]
	// Сначала — запись в базу: без неё файл не восстановится после перезапуска и не попадёт
	// в очистку. Запрос телевизора могут отменить, а запись должна дойти.
	path := enginePath(s.eng.DownloadsDir(), info, ih, info.UpvertedFiles()[index])
	if err := s.reg.MarkStored(context.WithoutCancel(ctx), ih, index, path, f.Length(), s.now()); err != nil {
		return err
	}
	episode := len(playableFiles(allFiles(t))) > 1
	bitrate := estimateBitrate(f.Length(), episode)
	head, tail := headTailBytes(f.Length(), bitrate)
	p := &prepared{
		bitrate: bitrate,
		head:    spanFor(info.PieceLength, f.Offset(), head),
		tail:    spanFor(info.PieceLength, f.Offset()+f.Length()-tail, tail),
	}
	ss.prepared[index] = p
	ss.stored = true
	ss.storedFiles[index] = true

	// Хранимые файлы (и выбранные раньше, и восстановленные после перезапуска) докачиваются,
	// остальные серии — только когда их откроют.
	for i, other := range files {
		if !ss.storedFiles[i] {
			other.SetPriority(torrent.PiecePriorityNone)
		}
	}
	f.SetPriority(torrent.PiecePriorityNormal)
	// Начало и конец — первыми: без конца файла MKV/AVI/MP4 плеер не может перематывать.
	for _, sp := range []pieceSpan{p.head, p.tail} {
		for i := sp.begin; i < sp.end; i++ {
			t.Piece(i).SetPriority(torrent.PiecePriorityHigh)
		}
	}
	return nil
}

// FileStatus — готовность выбранного файла; false, если для него не вызывали Prepare.
func (s *Service) FileStatus(ih metainfo.Hash, index int) (FileStatus, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ss, ok := s.sessions[ih]
	if !ok {
		return FileStatus{}, false
	}
	p, ok := ss.prepared[index]
	if !ok {
		return FileStatus{}, false
	}
	now := s.now()
	s.observe(ss, now) // обновить «с какого момента нет пиров»
	t := ss.t
	info := t.Info()
	f := t.Files()[index]
	var done, total int64
	for _, sp := range []pieceSpan{p.head, p.tail} {
		for i := sp.begin; i < sp.end; i++ {
			n := info.Piece(i).Length()
			total += n
			if t.PieceState(i).Complete {
				done += n
			}
		}
	}
	st := FileStatus{
		State:         FileBuffering,
		BufferPercent: bufferPercent(done, total),
		Peers:         t.Stats().ActivePeers,
		Speed:         int64(ss.speed),
		SmoothInSec:   smoothInSec(f.Length()-f.BytesCompleted(), f.Length(), p.bitrate, ss.speed),
		StreamPath:    streamPath(ih, index, f.DisplayPath()),
	}
	switch {
	case done == total:
		st.State = FileReady
	case st.Peers == 0 && !ss.noPeersSince.IsZero() && now.Sub(ss.noPeersSince) >= s.noPeersAfter:
		// Список файлов есть (раздача из .torrent или восстановлена), а буфер не набирается:
		// раздающих нет. Раздачу не убираем — появятся пиры, буфер доберётся.
		st.State = FileError
		st.Error = errNoPeers
	}
	return st, true
}

// streamPath — путь потока. Имя в конце — для плееров, которые узнают формат по расширению.
func streamPath(ih metainfo.Hash, index int, name string) string {
	return fmt.Sprintf("/stream/%s/%d/%s", ih.HexString(), index, url.PathEscape(baseName(name)))
}
