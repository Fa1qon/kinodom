package torrents

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"io/fs"
	"os"
	"time"

	"github.com/anacrolix/torrent/metainfo"
)

const (
	maintainEvery = 5 * time.Minute // проверка места при докачках (спека, раздел 9)
	expireEvery   = 24 * time.Hour  // очистка по сроку хранения
	idleFor       = time.Hour       // открытая, но не выбранная раздача без дела — убирается
)

// maintain — уборка по расписанию (Run зовёт её раз в 5 минут): раздачи с диска, который снова
// подключили, место при докачках, раздачи без файлов; раз в сутки — срок хранения.
func (s *Service) maintain(ctx context.Context) error {
	if err := s.restore(ctx); err != nil {
		return err
	}
	if err := s.checkSpace(ctx); err != nil {
		return err
	}
	if err := s.sweep(ctx); err != nil {
		return err
	}
	if err := s.limitSeeding(ctx); err != nil {
		return err
	}
	if s.now().Sub(s.expiredAt) < expireEvery {
		return nil
	}
	if err := s.expire(ctx); err != nil {
		return err
	}
	s.expiredAt = s.now()
	return nil
}

// releaseOpened — последнее открытие раздачи по её хранимым файлам; нулевое — ни разу.
func releaseOpened(files []StoredFile) map[metainfo.Hash]time.Time {
	out := map[metainfo.Hash]time.Time{}
	for _, f := range files {
		if f.LastOpened.After(out[f.InfoHash]) {
			out[f.InfoHash] = f.LastOpened
		} else if _, ok := out[f.InfoHash]; !ok {
			out[f.InfoHash] = time.Time{}
		}
	}
	return out
}

// expire удаляет раздачи целиком, которые не открывали дольше срока хранения (по умолчанию 14 дней;
// спека этапа 9, раздел 5.8): срок — от последнего открытия любого файла раздачи; ни разу не
// открытую раздачу не трогает; раздача, файл которой смотрят, ждёт следующей проверки.
func (s *Service) expire(ctx context.Context) error {
	files, err := s.reg.StoredByAge(ctx)
	if err != nil {
		return err
	}
	opened := releaseOpened(files)
	cut := s.now().Add(-s.pol().KeepFor)
	busy := map[metainfo.Hash]bool{}
	s.mu.Lock()
	for _, f := range files {
		if s.watching(s.sessions[f.InfoHash], f.Index, f.LastStream) {
			busy[f.InfoHash] = true
		}
	}
	s.mu.Unlock()
	for _, f := range files {
		o := opened[f.InfoHash]
		if o.IsZero() || !o.Before(cut) || busy[f.InfoHash] {
			continue
		}
		switch err := s.DeleteFile(ctx, f.InfoHash, f.Index); {
		case err == nil:
			s.log.Info("срок хранения вышел — файл удалён", "path", f.Path, "opened", f.LastOpened)
		case errors.Is(err, errDirMissing):
			if err := s.forgetMissing(ctx, f); err != nil {
				return err
			}
		case errors.Is(err, ErrWatching), errors.Is(err, ErrNotStored):
		default:
			s.log.Warn("файл не удалился по сроку хранения", "path", f.Path, "err", err)
		}
	}
	return nil
}

// sweep убирает раздачи без хранимых файлов (хвост этапа 2): открытые, но не выбранные дольше
// часа (в их папке — пустые разрежённые файлы), и записи о раздачах, которых нет среди открытых
// (ошибка «нет раздающих», папка, которая не удалилась раньше).
func (s *Service) sweep(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for ih, ss := range s.sessions {
		if len(ss.storedFiles) > 0 || ss.wantAll || !s.idle(ih, ss) {
			continue // «Скачать» ждёт списка файлов — раздача нужна, даже если о ней не спрашивают
		}
		dir := ""
		if info := ss.t.Info(); info != nil {
			dir = torrentDir(s.eng.TorrentDir(ih), info, ih)
		}
		s.forgetLocked(ctx, ih, dir)
	}
	recs, err := s.reg.Unstored(ctx, now.Add(-idleFor))
	if err != nil {
		return err
	}
	for _, rec := range recs {
		if _, open := s.sessions[rec.InfoHash]; open {
			continue
		}
		s.forgetLocked(ctx, rec.InfoHash, s.folderOf(rec))
	}
	return nil
}

// folderOf — папка раздачи по записи в базе; пусто — метаинфо нет, на диске ничего не создано.
func (s *Service) folderOf(rec Record) string {
	if rec.Metainfo == nil {
		return ""
	}
	mi, err := metainfo.Load(bytes.NewReader(rec.Metainfo))
	if err != nil {
		return ""
	}
	info, err := mi.UnmarshalInfo()
	if err != nil {
		return ""
	}
	return torrentDir(cmp.Or(rec.Dir, s.eng.DownloadsDir()), &info, rec.InfoHash)
}

// forgetMissing снимает запись о файле, которого нет на диске (диск не вернулся, папку удалили),
// когда его и так не открывали дольше срока хранения: иначе запись и баннер «папка недоступна»
// жили бы вечно. Файл, который есть (раздача не в движке по другой причине), не трогается.
func (s *Service) forgetMissing(ctx context.Context, f StoredFile) error {
	if _, err := os.Stat(f.Path); !errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err := s.reg.Unstore(ctx, f.InfoHash, f.Index); err != nil {
		return err
	}
	s.log.Info("файла нет на диске, срок хранения вышел — запись снята", "path", f.Path)
	left, err := s.reg.StoredFiles(ctx, f.InfoHash)
	if err != nil || len(left) > 0 {
		return err
	}
	s.mu.Lock()
	_, open := s.sessions[f.InfoHash]
	s.mu.Unlock()
	if open {
		return nil // раздача в движке — её уберёт уборка вместе с сессией
	}
	return s.reg.Forget(ctx, f.InfoHash)
}
