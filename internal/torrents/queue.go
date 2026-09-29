package torrents

import (
	"context"
	"slices"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
)

// Очередь загрузки (спека этапа 7, раздел 5.5): «Скачать» делает файлы раздачи хранимыми, а качается
// из них в каждый момент один — «в фокусе»; остальные ждут очереди в порядке серий. «Смотреть»
// переносит фокус на свой файл. Файл, который смотрят с другого телевизора, качается тоже.

// Readiness — цвет кнопки «Смотреть» на экране раздачи.
type Readiness string

const (
	ReadyNone   Readiness = "none"   // файл не хранится: кнопки нет
	ReadyWait   Readiness = "wait"   // жёлтая: смотреть можно, но будут остановки
	ReadySmooth Readiness = "smooth" // синяя: докачается раньше, чем досмотрится
	ReadyDone   Readiness = "done"   // зелёная: скачан целиком
)

// readinessOf — цвет кнопки и через сколько секунд смотреть без остановок (−1 — скорости нет,
// оценить нельзя). Скорость — всей раздачи: столько файл и получит, когда окажется в фокусе.
func readinessOf(stored bool, done, size int64, bitrate, speed float64) (Readiness, int) {
	switch {
	case !stored:
		return ReadyNone, 0
	case done >= size:
		return ReadyDone, 0
	}
	wait := smoothInSec(size-done, size, bitrate, speed)
	if wait == 0 {
		return ReadySmooth, 0
	}
	return ReadyWait, wait
}

// FileProgress — видеофайл раздачи с прогрессом для экрана раздачи.
type FileProgress struct {
	FileInfo
	Done      int64     `json:"done"` // байт скачано
	Percent   int       `json:"percent"`
	Stored    bool      `json:"stored"`   // хранится и докачивается
	Queued    bool      `json:"queued"`   // хранится, не докачан и ждёт очереди
	Watching  bool      `json:"watching"` // сейчас идёт поток
	Readiness Readiness `json:"readiness"`
	WaitSec   int       `json:"waitSec"` // «Без остановок через ~N мин»; −1 — оценить нельзя
	// Head и Tail — сколько процентов файла скачано подряд с начала и с конца: плееру нужны именно
	// они (полоса буфера). Считаются только у файла в фокусе.
	Head int `json:"head,omitempty"`
	Tail int `json:"tail,omitempty"`
}

// Download — «Скачать»: файлы хранятся и докачиваются по одному, в порядке серий. files == nil —
// все видеофайлы раздачи. Списка файлов ещё нет (magnet) — намерение «скачать всё» запоминается и
// применится, когда метаинфо придёт, в том числе после перезапуска. Повторный вызов ничего не
// меняет. Места не хватит даже после очистки — ErrLowSpace.
func (s *Service) Download(ctx context.Context, ih metainfo.Hash, files []int) error {
	s.spaceMu.Lock()
	defer s.spaceMu.Unlock()
	s.mu.Lock()
	ss, ok := s.sessions[ih]
	if !ok {
		s.mu.Unlock()
		return ErrNotOpen
	}
	info := ss.t.Info()
	if info == nil {
		if files != nil {
			s.mu.Unlock()
			return ErrNoInfo // номер файла без списка файлов не понять
		}
		ss.wantAll = true
		s.mu.Unlock()
		return s.reg.SetDownloadAll(ctx, ih, true)
	}
	all := ss.t.Files()
	if files == nil {
		for _, f := range playableFiles(allFiles(ss.t)) {
			files = append(files, f.Index)
		}
	}
	var need int64
	for _, i := range files {
		if i < 0 || i >= len(all) {
			s.mu.Unlock()
			return ErrNoSuchFile
		}
		if !ss.storedFiles[i] {
			need += all[i].Length() - all[i].BytesCompleted()
		}
	}
	dir := s.eng.TorrentDir(ih)
	s.mu.Unlock()
	if need > 0 {
		if err := s.ensureSpace(ctx, dir, need); err != nil {
			return err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if ss, ok = s.sessions[ih]; !ok {
		return ErrNotOpen
	}
	for _, i := range files {
		if err := s.storeLocked(ctx, ss, i); err != nil {
			return err
		}
	}
	if ss.wantAll {
		ss.wantAll = false
		if err := s.reg.SetDownloadAll(context.WithoutCancel(ctx), ih, false); err != nil {
			return err
		}
	}
	if ss.focus < 0 || !ss.storedFiles[ss.focus] || fileDone(all[ss.focus]) {
		s.setFocusLocked(ss, s.nextFocusLocked(ss))
	}
	s.wakeLocked(ss)
	s.applyLocked(ss)
	return nil
}

// storeLocked делает файл хранимым: запись в базу (без неё файл не восстановится после перезапуска
// и не попадёт в очистку) и в сессию. Вызывать под s.mu.
func (s *Service) storeLocked(ctx context.Context, ss *session, i int) error {
	if ss.storedFiles[i] {
		return nil
	}
	info := ss.t.Info()
	ih := ss.t.InfoHash()
	path := enginePath(s.eng.TorrentDir(ih), info, ih, info.UpvertedFiles()[i])
	// Запрос телевизора могут отменить, а запись должна дойти.
	if err := s.reg.MarkStored(context.WithoutCancel(ctx), ih, i, path, ss.t.Files()[i].Length(), s.now()); err != nil {
		return err
	}
	ss.storedFiles[i] = true
	ss.stored = true
	return nil
}

func fileDone(f *torrent.File) bool { return f.BytesCompleted() >= f.Length() }

// nextFocusLocked — следующий файл очереди: первый недокачанный хранимый после нынешнего фокуса по
// порядку серий (натуральная сортировка имён), по кругу. −1 — качать нечего. Вызывать под s.mu.
func (s *Service) nextFocusLocked(ss *session) int {
	files := ss.t.Files()
	order := make([]int, 0, len(ss.storedFiles))
	for i := range ss.storedFiles {
		order = append(order, i)
	}
	slices.SortFunc(order, func(a, b int) int {
		switch {
		case naturalLess(files[a].DisplayPath(), files[b].DisplayPath()):
			return -1
		case naturalLess(files[b].DisplayPath(), files[a].DisplayPath()):
			return 1
		}
		return a - b
	})
	start := slices.Index(order, ss.focus) + 1 // фокуса нет (−1) — с начала
	for k := range order {
		i := order[(start+k)%len(order)]
		if !fileDone(files[i]) {
			return i
		}
	}
	return -1
}

// setFocusLocked переносит фокус и запоминает его в базе: после перезапуска качается тот же файл.
// Вызывать под s.mu.
func (s *Service) setFocusLocked(ss *session, i int) {
	if ss.focus == i {
		return
	}
	ss.focus = i
	if err := s.reg.SetFocus(context.Background(), ss.t.InfoHash(), i); err != nil {
		s.log.Warn("фокус загрузки не записался", "hash", ss.t.InfoHash().HexString(), "err", err)
	}
}

// applyLocked — приоритеты файлов раздачи по правилам очереди: тело качается у файла в фокусе и у
// файлов, которые смотрят (потоку не должно не хватать кусков); остальные хранимые недокачанные
// ждут очереди. Начало и конец выбранных для просмотра файлов остаются «высокими» (prepare): иначе
// телевизор, выбравший серию, не дождался бы буфера, пока фокус у другой. Перепроверка кусков
// (повреждённый файл отметок) и пауза по месту — «не качать». Вызывать под s.mu.
func (s *Service) applyLocked(ss *session) {
	if ss.t.Info() == nil {
		return
	}
	verifying := len(ss.verifyQ) > 0
	for i, f := range ss.t.Files() {
		switch {
		case !ss.storedFiles[i] || verifying || ss.paused[i]:
			f.SetPriority(torrent.PiecePriorityNone)
		case i == ss.focus || ss.readers[i] > 0 || fileDone(f):
			f.SetPriority(torrent.PiecePriorityNormal)
		default:
			f.SetPriority(torrent.PiecePriorityNone)
		}
	}
}

// advanceLocked — фокус докачался: следующий файл очереди. Вызывать под s.mu (раз в секунду).
func (s *Service) advanceLocked(ss *session) {
	if ss.t.Info() == nil || len(ss.storedFiles) == 0 {
		return
	}
	files := ss.t.Files()
	if ss.focus >= 0 && ss.storedFiles[ss.focus] && !fileDone(files[ss.focus]) {
		return
	}
	if next := s.nextFocusLocked(ss); next != ss.focus {
		s.setFocusLocked(ss, next)
		s.applyLocked(ss)
	}
}

// progressLocked — прогресс видеофайлов раздачи для экрана раздачи. Вызывать под s.mu.
func (s *Service) progressLocked(ss *session) []FileProgress {
	t := ss.t
	playable := playableFiles(allFiles(t))
	files := t.Files()
	out := make([]FileProgress, len(playable))
	for k, fi := range playable {
		f := files[fi.Index]
		done := min(f.BytesCompleted(), f.Length())
		p := FileProgress{FileInfo: fi, Done: done, Percent: bufferPercent(done, f.Length()), Stored: ss.storedFiles[fi.Index],
			Watching: ss.readers[fi.Index] > 0}
		bitrate := estimateBitrate(f.Length(), len(playable) > 1)
		if pr, ok := ss.prepared[fi.Index]; ok {
			bitrate = pr.bitrate
		}
		p.Readiness, p.WaitSec = readinessOf(p.Stored, done, f.Length(), bitrate, ss.speed)
		p.Queued = p.Stored && done < f.Length() && fi.Index != ss.focus && !p.Watching
		if fi.Index == ss.focus {
			p.Head, p.Tail = contiguous(t, f)
		}
		out[k] = p
	}
	return out
}

// contiguous — сколько процентов файла скачано подряд с начала и с конца.
func contiguous(t *torrent.Torrent, f *torrent.File) (head, tail int) {
	info := t.Info()
	begin, end := f.BeginPieceIndex(), f.EndPieceIndex()
	overlap := func(i int) int64 {
		p := info.Piece(i)
		return max(0, min(p.Offset()+p.Length(), f.Offset()+f.Length())-max(p.Offset(), f.Offset()))
	}
	var h, tl int64
	i := begin
	for ; i < end && t.PieceState(i).Complete; i++ {
		h += overlap(i)
	}
	for j := end - 1; j >= i && t.PieceState(j).Complete; j-- {
		tl += overlap(j)
	}
	return bufferPercent(h, f.Length()), bufferPercent(tl, f.Length())
}
