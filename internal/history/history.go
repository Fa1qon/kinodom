// Package history — история просмотров по устройствам (спека этапа 8, раздел 7): где остановились в
// каждом файле раздачи, просмотрен ли он, когда смотрели. Место приходит от потока (VLC — примерно,
// по месту чтения файла) или от своего плеера (точно).
package history

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"kinodom/internal/store"
)

// Пределы (спека этапа 8, разделы 7.1 и 7.3).
const (
	WatchedAt   = 0.9  // дошли до 90 % файла — просмотрен
	resumeFrom  = 60.0 // продолжать, если остановились дальше первой минуты
	resumeUntil = 0.95 // … и не в самом конце
	resumeBack  = 10.0 // открыть за 10 с до места
)

// Service — история просмотров.
type Service struct {
	db  *store.DB
	now func() time.Time
}

func New(db *store.DB) *Service { return &Service{db: db, now: time.Now} }

// FileProgress — место в файле раздачи у устройства.
type FileProgress struct {
	Index       int       `json:"index"`
	Fraction    float64   `json:"fraction"`
	PositionSec float64   `json:"positionSec"` // 0 — неизвестно
	DurationSec float64   `json:"durationSec"` // 0 — неизвестна
	Watched     bool      `json:"watched"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// ErrBadPosition — место или длительность не годятся.
var ErrBadPosition = errors.New("место и длительность — неотрицательные числа, место не больше длительности")

// Duration — длительность файла, если известна.
func (s *Service) Duration(ctx context.Context, hash string, index int) (float64, bool) {
	var d float64
	err := s.db.R.QueryRowContext(ctx, `SELECT duration_sec FROM media_durations WHERE hash = ? AND file_index = ?`, hash, index).Scan(&d)
	return d, err == nil && d > 0
}

// SetDuration — длительность файла (из заголовка или от плеера).
func (s *Service) SetDuration(ctx context.Context, hash string, index int, sec float64) error {
	if sec <= 0 {
		return ErrBadPosition
	}
	_, err := s.db.W.ExecContext(ctx, `INSERT INTO media_durations (hash, file_index, duration_sec) VALUES (?, ?, ?)
		ON CONFLICT(hash, file_index) DO UPDATE SET duration_sec = excluded.duration_sec`, hash, index, sec)
	return err
}

// save — место в файле: просмотрен остаётся просмотренным (перемотали назад — отметка не пропадает).
func (s *Service) save(ctx context.Context, device, hash string, index int, fraction, pos, dur float64) error {
	watched := fraction >= WatchedAt
	_, err := s.db.W.ExecContext(ctx, `INSERT INTO watch_progress (device, hash, file_index, fraction, position_sec, duration_sec, watched, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(device, hash, file_index) DO UPDATE SET fraction = excluded.fraction, position_sec = excluded.position_sec,
		duration_sec = excluded.duration_sec, watched = watched OR excluded.watched, updated_at = excluded.updated_at`,
		device, hash, index, fraction, pos, dur, watched, s.now().UnixMilli())
	return err
}

// Report — место по чтению потока (спека этапа 8, раздел 7.2): offset — докуда плеер дочитал файл
// размером size. Ошибка записи не мешает потоку — её некуда вернуть.
func (s *Service) Report(ctx context.Context, device, hash string, index int, offset, size int64) {
	if device == "" || size <= 0 {
		return
	}
	fraction := min(max(float64(offset)/float64(size), 0), 1)
	pos := 0.0
	dur, ok := s.Duration(ctx, hash, index)
	if ok {
		pos = fraction * dur
	} else {
		dur = 0
	}
	_ = s.save(ctx, device, hash, index, fraction, pos, dur)
}

// SetPosition — точное место от своего плеера.
func (s *Service) SetPosition(ctx context.Context, device, hash string, index int, pos, dur float64) error {
	if pos < 0 || dur <= 0 || pos > dur {
		return ErrBadPosition
	}
	if err := s.SetDuration(ctx, hash, index, dur); err != nil {
		return err
	}
	return s.save(ctx, device, hash, index, pos/dur, pos, dur)
}

// SetWatched — «Просмотрено» / «Не просмотрено» из пульта; «Не просмотрено» сбрасывает и место.
func (s *Service) SetWatched(ctx context.Context, device, hash string, index int, watched bool) error {
	dur, _ := s.Duration(ctx, hash, index)
	var err error
	if watched {
		_, err = s.db.W.ExecContext(ctx, `INSERT INTO watch_progress (device, hash, file_index, fraction, position_sec, duration_sec, watched, updated_at)
			VALUES (?, ?, ?, 0, 0, ?, 1, ?)
			ON CONFLICT(device, hash, file_index) DO UPDATE SET watched = 1, updated_at = excluded.updated_at`,
			device, hash, index, dur, s.now().UnixMilli())
	} else {
		_, err = s.db.W.ExecContext(ctx, `UPDATE watch_progress SET watched = 0, fraction = 0, position_sec = 0, updated_at = ?
			WHERE device = ? AND hash = ? AND file_index = ?`, s.now().UnixMilli(), device, hash, index)
	}
	return err
}

// Files — места во всех файлах раздачи у устройства, по номеру файла.
func (s *Service) Files(ctx context.Context, device, hash string) ([]FileProgress, error) {
	rows, err := s.db.R.QueryContext(ctx, `SELECT file_index, fraction, position_sec, duration_sec, watched, updated_at FROM watch_progress
		WHERE device = ? AND hash = ? ORDER BY file_index`, device, hash)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []FileProgress{}
	for rows.Next() {
		var f FileProgress
		var at int64
		if err := rows.Scan(&f.Index, &f.Fraction, &f.PositionSec, &f.DurationSec, &f.Watched, &at); err != nil {
			return nil, err
		}
		f.UpdatedAt = time.UnixMilli(at)
		out = append(out, f)
	}
	return out, rows.Err()
}

// StartSec — откуда открыть плеер (спека этапа 8, раздел 7.3): за 10 с до места, если файл не
// просмотрен и остановились дальше первой минуты, но не в самом конце; иначе с начала (0).
func (s *Service) StartSec(ctx context.Context, device, hash string, index int) int {
	var pos, fraction float64
	var watched bool
	err := s.db.R.QueryRowContext(ctx, `SELECT position_sec, fraction, watched FROM watch_progress WHERE device = ? AND hash = ? AND file_index = ?`,
		device, hash, index).Scan(&pos, &fraction, &watched)
	if errors.Is(err, sql.ErrNoRows) || err != nil || watched || pos < resumeFrom || fraction >= resumeUntil {
		return 0
	}
	return int(pos - resumeBack)
}

// Item — раздача в истории устройства.
type Item struct {
	Hash      string       `json:"hash"`
	Last      FileProgress `json:"last"`    // файл, который смотрели последним
	Watched   int          `json:"watched"` // просмотрено файлов
	Files     int          `json:"files"`   // файлов с отметками
	UpdatedAt time.Time    `json:"updatedAt"`
}

// List — история устройства: раздачи по времени последнего просмотра, новые первыми.
func (s *Service) List(ctx context.Context, device string) ([]Item, error) {
	rows, err := s.db.R.QueryContext(ctx, `SELECT hash, file_index, fraction, position_sec, duration_sec, watched, updated_at FROM watch_progress
		WHERE device = ? ORDER BY updated_at DESC, file_index DESC`, device)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Item{}
	pos := map[string]int{}
	for rows.Next() {
		var h string
		var f FileProgress
		var at int64
		if err := rows.Scan(&h, &f.Index, &f.Fraction, &f.PositionSec, &f.DurationSec, &f.Watched, &at); err != nil {
			return nil, err
		}
		f.UpdatedAt = time.UnixMilli(at)
		i, ok := pos[h]
		if !ok {
			i = len(out)
			pos[h] = i
			out = append(out, Item{Hash: h, Last: f, UpdatedAt: f.UpdatedAt})
		}
		out[i].Files++
		if f.Watched {
			out[i].Watched++
		}
	}
	return out, rows.Err()
}

// Remove — убрать раздачу из истории устройства.
func (s *Service) Remove(ctx context.Context, device, hash string) error {
	_, err := s.db.W.ExecContext(ctx, `DELETE FROM watch_progress WHERE device = ? AND hash = ?`, device, hash)
	return err
}
