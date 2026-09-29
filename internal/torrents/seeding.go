package torrents

import (
	"cmp"
	"context"
)

const (
	defaultMaxSeeding = 10   // раздаются не больше 10 раздач (спека, раздел 9)
	streamUploadShare = 0.25 // во время просмотра — четверть лимита отдачи
)

// NoUpload — лимит отдачи «не раздавать» (0 в настройках, спека этапа 7, раздел 5.5).
const NoUpload = -1.0

// SetUploadLimit — лимит отдачи из настроек, байт/с: 0 — без ограничения, NoUpload — не раздавать.
// Действует со следующего такта цикла Run, без перезапуска.
func (s *Service) SetUploadLimit(bytesPerSec float64) {
	s.mu.Lock()
	s.uploadBase, s.uploadSet = bytesPerSec, true
	s.mu.Unlock()
}

// UploadLimit — лимит отдачи из настроек, байт/с: заданный на ходу, иначе при создании движка.
func (s *Service) UploadLimit() float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.uploadSet {
		return s.uploadBase
	}
	return s.eng.cfg.UploadLimit
}

// shapeUpload — лимит отдачи: пока идёт хоть один поток (любого модуля), четверть лимита из
// настроек — иначе отдача забивает канал, и фильм тормозит (спека, раздел 9). Без лимита
// (0) — без лимита и во время просмотра. «Не раздавать» — раздачи не отдают куски вовсе.
func (s *Service) shapeUpload() {
	base := s.UploadLimit()
	s.setUploadOff(base < 0)
	limit := max(base, 0)
	if s.keeper.Active() > 0 || s.ActiveStreams() > 0 {
		limit = limit * streamUploadShare
	}
	if limit != s.upload {
		s.eng.SetUploadLimit(limit)
		s.upload = limit
	}
}

// setUploadOff включает и выключает «не раздавать» у всех раздач. Молчащие раздачи не отдают и
// так — их будит только wakeLocked.
func (s *Service) setUploadOff(off bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if off == s.uploadOff {
		return
	}
	s.uploadOff = off
	for _, ss := range s.sessions {
		switch {
		case off:
			ss.t.DisallowDataUpload()
		case !ss.quiet:
			ss.t.AllowDataUpload()
		}
	}
}

// limitSeeding — раздаются не больше MaxSeeding раздач с самым свежим открытием; остальные
// скачанные раздачи молчат: без соединений и без отдачи (итого ~200 соединений на всё). Раздачи,
// которые докачиваются или смотрят, не трогаются: им нужны пиры.
func (s *Service) limitSeeding(ctx context.Context) error {
	order, err := s.reg.TorrentsByOpened(ctx)
	if err != nil {
		return err
	}
	top := map[string]bool{}
	for _, ih := range order[:min(len(order), s.pol().MaxSeeding)] {
		top[ih.HexString()] = true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for ih, ss := range s.sessions {
		if len(ss.storedFiles) == 0 || ss.t.Info() == nil {
			continue // раздачу только открыли — ей нужны пиры
		}
		quiet := !top[ih.HexString()] && !ss.downloading() && !ss.streaming()
		if quiet == ss.quiet {
			continue
		}
		if quiet {
			ss.t.DisallowDataUpload()
			ss.t.SetMaxEstablishedConns(0)
			ss.quiet = true
		} else {
			s.wakeLocked(ss)
		}
	}
	return nil
}

// downloading — какой-то хранимый файл раздачи ещё не докачан. Вызывать под s.mu.
func (ss *session) downloading() bool {
	files := ss.t.Files()
	for i := range ss.storedFiles {
		if files[i].BytesCompleted() < files[i].Length() {
			return true
		}
	}
	return false
}

// wakeLocked возвращает «молчащей» раздаче соединения и отдачу. Вызывать под s.mu.
func (s *Service) wakeLocked(ss *session) {
	if !ss.quiet {
		return
	}
	if !s.uploadOff {
		ss.t.AllowDataUpload()
	}
	ss.t.SetMaxEstablishedConns(cmp.Or(s.eng.cfg.ConnsPerTorrent, 20))
	ss.quiet = false
}
