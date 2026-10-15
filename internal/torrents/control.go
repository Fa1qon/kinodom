package torrents

import (
	"context"
	"errors"

	"github.com/anacrolix/torrent/metainfo"
)

var (
	ErrDownloadPaused  = errors.New("загрузка уже на паузе")
	ErrDownloadRunning = errors.New("загрузка уже выполняется")
)

// PauseDownload временно убирает выбранный файл из очереди. Просмотр нельзя прервать
// управлением загрузками: поток сам держит необходимые куски.
func (s *Service) PauseDownload(ctx context.Context, ih metainfo.Hash, index int) error {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	ss, ok := s.sessions[ih]
	if !ok {
		return ErrNotOpen
	}
	if ss.t.Info() == nil || index < 0 || index >= len(ss.t.Files()) {
		return ErrNoSuchFile
	}
	if !ss.storedFiles[index] {
		return ErrNotStored
	}
	if ss.readers[index] > 0 {
		return ErrWatching
	}
	if ss.paused[index] {
		return ErrDownloadPaused
	}
	if fileDone(ss.t.Files()[index]) {
		return ErrDownloadDone
	}
	ss.paused[index] = true
	if ss.focus == index {
		s.setFocusLocked(ss, s.nextFocusLocked(ss))
	}
	s.applyLocked(ss)
	s.wakeLocked(ss)
	return nil
}

// ResumeDownload возвращает файл в очередь. Если текущего фокуса нет, этот файл
// становится следующим к загрузке.
func (s *Service) ResumeDownload(ctx context.Context, ih metainfo.Hash, index int) error {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	ss, ok := s.sessions[ih]
	if !ok {
		return ErrNotOpen
	}
	if ss.t.Info() == nil || index < 0 || index >= len(ss.t.Files()) {
		return ErrNoSuchFile
	}
	if !ss.storedFiles[index] {
		return ErrNotStored
	}
	if fileDone(ss.t.Files()[index]) {
		return ErrDownloadDone
	}
	if !ss.paused[index] {
		return ErrDownloadRunning
	}
	delete(ss.paused, index)
	if ss.focus < 0 || ss.focus >= len(ss.t.Files()) || ss.paused[ss.focus] || fileDone(ss.t.Files()[ss.focus]) {
		s.setFocusLocked(ss, index)
	}
	s.applyLocked(ss)
	s.wakeLocked(ss)
	return nil
}

// ErrDownloadDone сообщает, что управлять уже полностью скачанным файлом не нужно.
var ErrDownloadDone = errors.New("файл уже скачан")
