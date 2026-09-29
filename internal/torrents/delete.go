package torrents

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
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
	errDirMissing = errors.New("папка загрузок раздачи недоступна — файл удалится, когда диск вернётся")
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
	return s.deleteLocked(ctx, ih, index)
}

func (s *Service) deleteLocked(ctx context.Context, ih metainfo.Hash, index int) error {
	sf, ok, err := s.reg.StoredFile(ctx, ih, index)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotStored
	}
	ss := s.sessions[ih]
	if s.watching(ss, index, sf.LastStream) {
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
	if err := removeRetry(path); err != nil {
		return fmt.Errorf("файл %s не удаляется: %w", path, err)
	}
	// Пустой разрежённый файл — сразу, до сброса отметок: кусок на границе общий с соседним
	// файлом, движок докачает его для соседа и иначе создал бы удалённый файл полного размера.
	if err := createSparse(path, f.Length()); err != nil {
		return fmt.Errorf("файл %s: %w", path, err)
	}
	for i := f.BeginPieceIndex(); i < f.EndPieceIndex(); i++ {
		if err := s.eng.pc.Set(metainfo.PieceKey{InfoHash: ih, Index: i}, false); err != nil {
			return fmt.Errorf("отметки кусков: %w", err)
		}
		t.Piece(i).UpdateCompletion()
	}
	if err := s.reg.Unstore(ctx, ih, index); err != nil {
		return err
	}
	if ss != nil {
		delete(ss.prepared, index)
		delete(ss.storedFiles, index)
		ss.stored = len(ss.storedFiles) > 0
	}
	left, err := s.reg.StoredFiles(ctx, ih)
	if err != nil {
		return err
	}
	if len(left) == 0 {
		s.forgetLocked(ctx, ih, torrentDir(s.eng.TorrentDir(ih), info, ih))
	}
	return nil
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

// removeRetry удаляет файл. Windows может ещё мгновение держать только что закрытый файл
// (антивирус, движок дочитывает кусок) — повтор до 3 с.
func removeRetry(path string) error {
	return retry(func() error {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	})
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
