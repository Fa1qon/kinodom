package torrents

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
)

var (
	// ErrWatching — файл сейчас смотрят: удалять нельзя (спека, раздел 9).
	ErrWatching = errors.New("Сейчас смотрят на другом телевизоре")
	// ErrNotStored — файл не скачан: его не выбирали для просмотра или уже удалили.
	ErrNotStored = errors.New("этот файл не скачан")
	// errDirMissing — раздачи нет в движке: её папка загрузок недоступна (диск не подключён).
	errDirMissing = errors.New("папка загрузок раздачи недоступна — подключите диск и удалите снова")
)

// watchingFor — «сейчас смотрят»: было потоковое подключение к файлу за последние 6 часов.
const watchingFor = 6 * time.Hour

// DeleteFile удаляет скачанный файл, не выгружая раздачу из движка: иначе у соседнего
// телевизора, который смотрит другую серию той же раздачи, оборвалась бы подкачка (спека,
// раздел 9; порядок проверен вживую — исследование, раздел 2). Файл, который смотрят, не
// удаляется. Последний хранимый файл уносит с собой и раздачу.
func (s *Service) DeleteFile(ctx context.Context, ih metainfo.Hash, index int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.deleteLocked(ctx, ih, index, true)
}

// DeleteRelease — корзина раздачи в «Загрузках»: удаляются все хранимые файлы раздачи, файл, который
// сейчас смотрят, пропускается (спека этапа 7, раздел 10.6). Ничего не хранится — ErrNotStored.
func (s *Service) DeleteRelease(ctx context.Context, ih metainfo.Hash) (deleted, skipped int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	files, err := s.reg.StoredFiles(ctx, ih)
	if err != nil {
		return 0, 0, err
	}
	if len(files) == 0 {
		return 0, 0, ErrNotStored
	}
	for _, i := range files {
		switch err := s.deleteLocked(ctx, ih, i, true); {
		case errors.Is(err, ErrWatching):
			skipped++
		case err != nil:
			return deleted, skipped, err
		default:
			deleted++
		}
	}
	return deleted, skipped, nil
}

// deleteBehind — удаление просмотренной серии позади, когда места не хватает: правила 6 часов нет,
// не удаляется только файл с открытым потоком (решение заказчика, этап 7a).
func (s *Service) deleteBehind(ctx context.Context, ih metainfo.Hash, index int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.deleteLocked(ctx, ih, index, false)
}

// deleteLocked — удаление файла. recent — «сейчас смотрят» и поток за последние 6 часов, иначе
// только открытый поток. Вызывать под s.mu.
func (s *Service) deleteLocked(ctx context.Context, ih metainfo.Hash, index int, recent bool) error {
	sf, ok, err := s.reg.StoredFile(ctx, ih, index)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotStored
	}
	ss := s.sessions[ih]
	if (recent && s.watching(ss, index, sf.LastStream)) || (ss != nil && ss.readers[index] > 0) {
		return ErrWatching
	}
	t, found := s.eng.cl.Torrent(ih)
	if !found || t.Info() == nil {
		return errDirMissing
	}
	info := t.Info()
	f := t.Files()[index]
	// Не качать ни файл, ни его куски. Кусок на границе с соседним файлом останется нужен соседу —
	// его приоритет задаёт сосед.
	f.SetPriority(torrent.PiecePriorityNone)
	for i := f.BeginPieceIndex(); i < f.EndPieceIndex(); i++ {
		t.Piece(i).SetPriority(torrent.PiecePriorityNone)
	}
	path := enginePath(s.eng.TorrentDir(ih), info, ih, info.UpvertedFiles()[index])
	// Освобождаются только скачанные куски целиком внутри файла: сначала отметка «не скачан»
	// (движок перестаёт их раздавать), потом обнуление на диске. Кусок на границе с соседним файлом
	// и недокачанные куски не трогаются: их байты нужны соседу или в них идёт приём, и сброс дал бы
	// несошедшийся хэш — за него anacrolix банит раздающего. Файл остаётся на месте разрежённым:
	// удалять и создавать его заново не нужно, и движок его не «воскресит».
	// Куски, ждущие перепроверки (повреждённый файл отметок), на диске почти наверняка есть —
	// они освобождаются так же и снимаются с очереди; проверяемый прямо сейчас — не трогается.
	var free []pieceSpan
	hadQueue := ss != nil && len(ss.verifyQ) > 0
	for i := f.BeginPieceIndex(); i < f.EndPieceIndex(); i++ {
		p := info.Piece(i)
		queued := ss != nil && ss.verifyQ[i] && s.verifyNow != (pieceRef{ih, i})
		if p.Offset() < f.Offset() || p.Offset()+p.Length() > f.Offset()+f.Length() || !(t.PieceState(i).Complete || queued) {
			continue
		}
		if queued {
			delete(ss.verifyQ, i)
		}
		if err := s.eng.pc.Set(metainfo.PieceKey{InfoHash: ih, Index: i}, false); err != nil {
			return fmt.Errorf("отметки кусков: %w", err)
		}
		t.Piece(i).UpdateCompletion()
		if n := len(free); n > 0 && free[n-1].end == i {
			free[n-1].end = i + 1
		} else {
			free = append(free, pieceSpan{i, i + 1})
		}
	}
	for _, sp := range free {
		from := info.Piece(sp.begin).Offset() - f.Offset()
		to := info.Piece(sp.end-1).Offset() + info.Piece(sp.end-1).Length() - f.Offset()
		if err := zeroRange(path, f.Length(), from, to); err != nil {
			return fmt.Errorf("файл %s: место не освободилось: %w", path, err)
		}
	}
	if err := s.reg.Unstore(ctx, ih, index); err != nil {
		return err
	}
	if ss != nil {
		delete(ss.prepared, index)
		delete(ss.storedFiles, index)
		delete(ss.paused, index)
		ss.stored = len(ss.storedFiles) > 0
		if hadQueue && len(ss.verifyQ) == 0 {
			s.verifyDone(ss)
		}
		if ss.focus == index { // удалили файл в фокусе — качается следующий
			s.setFocusLocked(ss, s.nextFocusLocked(ss))
			s.applyLocked(ss)
		}
	}
	left, err := s.reg.StoredFiles(ctx, ih)
	if err != nil {
		return err
	}
	// Раздачу без хранимых файлов выгружаем сразу, только если она без дела: иначе её листает
	// телевизор (или для неё идёт Prepare, ради которого чистили место) — её уберёт уборка позже.
	if len(left) == 0 && s.idle(ih, ss) {
		s.forgetLocked(ctx, ih, torrentDir(s.eng.TorrentDir(ih), info, ih))
	}
	return nil
}

// idle — раздачу можно выгрузить: о ней не спрашивали дольше часа, потоков нет, перепроверка её
// кусков не идёт. Вызывать под s.mu.
func (s *Service) idle(ih metainfo.Hash, ss *session) bool {
	if ss == nil {
		return true
	}
	return s.now().Sub(ss.lastSeen) >= idleFor && !ss.streaming() && len(ss.verifyQ) == 0 && s.verifyNow.ih != ih
}

// watching — файл сейчас смотрят: открыт поток или поток был за последние 6 часов.
func (s *Service) watching(ss *session, index int, lastStream time.Time) bool {
	if ss != nil && ss.readers[index] > 0 {
		return true
	}
	return !lastStream.IsZero() && s.now().Sub(lastStream) < watchingFor
}

// forgetLocked убирает раздачу без хранимых файлов: из движка, с диска (в папке — только пустые
// разрежённые файлы) и из базы. Если папка не удалилась, запись о раздаче остаётся — её подберёт
// следующая уборка. dir — папка раздачи; пусто — метаинфо не было, на диске ничего нет.
// Вызывать под s.mu.
func (s *Service) forgetLocked(ctx context.Context, ih metainfo.Hash, dir string) {
	if t, ok := s.eng.cl.Torrent(ih); ok {
		t.Drop()
		select {
		case <-t.Closed():
		case <-time.After(5 * time.Second):
		}
	}
	delete(s.sessions, ih)
	if dir != "" {
		if err := removeAllRetry(dir); err != nil {
			s.log.Warn("папка раздачи не удалилась — уберу при следующей уборке", "dir", dir, "err", err)
			return
		}
	}
	if err := s.reg.Forget(ctx, ih); err != nil {
		s.log.Warn("запись о раздаче не удалилась", "hash", ih.HexString(), "err", err)
	}
}

func removeAllRetry(dir string) error { return retry(func() error { return os.RemoveAll(dir) }) }

func retry(fn func() error) error {
	deadline := time.Now().Add(3 * time.Second)
	for {
		err := fn()
		if err == nil || time.Now().After(deadline) {
			return err
		}
		time.Sleep(50 * time.Millisecond)
	}
}
