package torrents

import (
	"cmp"
	"context"
)

const (
	defaultMaxSeeding = 10   // раздаются не больше 10 раздач (спека, раздел 9)
	streamUploadShare = 0.25 // во время просмотра — четверть лимита отдачи
)

// shapeUpload — лимит отдачи: пока идёт хоть один поток (любого модуля), четверть лимита из
// настроек — иначе отдача забивает канал, и фильм тормозит (спека, раздел 9). Без лимита
// (0) — без лимита и во время просмотра.
func (s *Service) shapeUpload() {
	base := s.eng.cfg.UploadLimit
	limit := base
	if s.keeper.Active() > 0 || s.ActiveStreams() > 0 {
		limit = base * streamUploadShare
	}
	if limit != s.upload {
		s.eng.SetUploadLimit(limit)
		s.upload = limit
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
	conns := cmp.Or(s.eng.cfg.ConnsPerTorrent, 20)
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
		} else {
			ss.t.AllowDataUpload()
			ss.t.SetMaxEstablishedConns(conns)
		}
		ss.quiet = quiet
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
