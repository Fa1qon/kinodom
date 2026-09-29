package torrents

import (
	"bytes"
	"cmp"
	"context"
	"errors"
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

// expire удаляет файлы, которые не открывали дольше срока хранения (по умолчанию 14 дней), кроме
// тех, что смотрят (спека, раздел 9).
func (s *Service) expire(ctx context.Context) error {
	files, err := s.reg.StoredByAge(ctx)
	if err != nil {
		return err
	}
	cut := s.now().Add(-s.pol().KeepFor)
	for _, f := range files {
		if !f.LastOpened.Before(cut) {
			break
		}
		switch err := s.DeleteFile(ctx, f.InfoHash, f.Index); {
		case err == nil:
			s.log.Info("срок хранения вышел — файл удалён", "path", f.Path, "opened", f.LastOpened)
		case errors.Is(err, ErrWatching), errors.Is(err, ErrNotStored), errors.Is(err, errDirMissing):
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
		if len(ss.storedFiles) > 0 || now.Sub(ss.lastSeen) < idleFor || ss.streaming() {
			continue
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
