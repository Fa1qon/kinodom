package history

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Журнал вкуса (view_log, миграция 0021): для рекомендаций история отвечает не «где остановились», а «кто
// что смотрел и чем кончилось». Отличия от watch_progress:
//   - по человеку: профиль семьи, на который переключено устройство, иначе само устройство;
//   - seen — доля, просмотренная подряд: продвижение засчитывается, только если место ушло вперёд не быстрее
//     воспроизведения (с запасом); перемотка к финалу не делает фильм «досмотренным»;
//   - сеансы, пересмотры и конец: удалил сам, убрала уборка, убрал из истории.

// Пределы журнала.
const (
	sessionGap   = 30 * time.Minute // перерыв больше — новый раз сел смотреть
	stepWindow   = 5 * time.Minute  // между сообщениями места дольше — продвижение не засчитывается
	stepSlack    = 20.0             // секунд запаса на шаг (буфер плеера, редкие сообщения)
	stepSpeed    = 1.5              // во сколько раз место может уходить быстрее часов (ускоренный просмотр)
	stepNoDur    = 0.02             // шаг без известной длительности
	finishedSeen = 0.5              // досмотрел: дошёл до 90 % и видел не меньше половины
	rewatchSeen  = 0.85             // столько seen — один полный просмотр (для счёта пересмотров)
)

// Ended — чем кончилось: удалил сам, уборка, убрал из истории.
const (
	EndedDeleted = "deleted"
	EndedExpired = "expired"
	EndedRemoved = "removed"
)

// SetOwner — кто смотрит на устройстве (профиль семьи); nil — само устройство.
func (s *Service) SetOwner(f func(ctx context.Context, device string) string) { s.owner = f }

// SetFilmOf — номер Кинопоиска фильма раздачи или единицы медиатеки; nil — журнал без фильмов.
func (s *Service) SetFilmOf(f func(ctx context.Context, hash string) int) { s.filmOf = f }

// Owner — хозяин журнала для устройства: «p:<профиль>» или «d:<устройство>».
func (s *Service) Owner(ctx context.Context, device string) string {
	if s.owner != nil {
		if o := s.owner(ctx, device); o != "" {
			return o
		}
	}
	return "d:" + device
}

func (s *Service) film(ctx context.Context, hash string) int {
	if s.filmOf == nil {
		return 0
	}
	return s.filmOf(ctx, hash)
}

// logView — новое место в журнале вкуса. Ошибка журнала не мешает истории — только возвращается.
func (s *Service) logView(ctx context.Context, device, hash string, index int, fraction, dur float64) error {
	owner := s.Owner(ctx, device)
	now := s.now()
	var seen, maxF, lastF float64
	var lastAt, kp int64
	err := s.db.W.QueryRowContext(ctx, `SELECT seen, max_fraction, last_fraction, last_at, kp_id FROM view_log WHERE owner = ? AND hash = ? AND file_index = ?`,
		owner, hash, index).Scan(&seen, &maxF, &lastF, &lastAt, &kp)
	fresh := errors.Is(err, sql.ErrNoRows)
	if err != nil && !fresh {
		return err
	}
	if kp == 0 {
		kp = int64(s.film(ctx, hash))
	}
	elapsed := now.Sub(time.UnixMilli(lastAt))
	allowed := func(sec float64) float64 {
		if dur <= 0 {
			return stepNoDur
		}
		return (sec*stepSpeed + stepSlack) / dur
	}
	if fresh {
		// первое место: засчитывается, только если начали с начала (а не открыли с середины)
		if fraction <= allowed(30) {
			seen = fraction
		}
		_, err = s.db.W.ExecContext(ctx, `INSERT INTO view_log (owner, hash, file_index, kp_id, first_at, last_at, seen, max_fraction, last_fraction)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, owner, hash, index, kp, now.UnixMilli(), now.UnixMilli(), seen, fraction, fraction)
		return err
	}
	if d := fraction - lastF; elapsed <= stepWindow && d > 0 && d <= allowed(elapsed.Seconds()) {
		seen += d
	}
	newSession := 0
	if elapsed > sessionGap {
		newSession = 1
	}
	maxF = max(maxF, fraction)
	finished, times := 0, 0
	if maxF >= WatchedAt && seen >= finishedSeen {
		finished = 1
		times = max(1, int(seen/rewatchSeen))
	}
	_, err = s.db.W.ExecContext(ctx, `UPDATE view_log SET kp_id = ?, last_at = ?, sessions = sessions + ?, seen = ?, max_fraction = ?,
		last_fraction = ?, finished = MAX(finished, ?), times = MAX(times, ?), ended = '', ended_at = 0
		WHERE owner = ? AND hash = ? AND file_index = ?`,
		kp, now.UnixMilli(), newSession, seen, maxF, fraction, finished, times, owner, hash, index)
	return err
}

// logMarked — «Просмотрено» из пульта: смотрел где-то ещё. Досмотренным считается, но seen не растёт —
// рекомендации отличают это от просмотра здесь.
func (s *Service) logMarked(ctx context.Context, device, hash string, index int) error {
	owner, now := s.Owner(ctx, device), s.now().UnixMilli()
	_, err := s.db.W.ExecContext(ctx, `INSERT INTO view_log (owner, hash, file_index, kp_id, first_at, last_at, sessions, max_fraction, last_fraction, finished, times)
		VALUES (?, ?, ?, ?, ?, ?, 0, 1, 1, 1, 1)
		ON CONFLICT(owner, hash, file_index) DO UPDATE SET finished = 1, times = MAX(times, 1), max_fraction = 1, last_at = excluded.last_at`,
		owner, hash, index, s.film(ctx, hash), now, now)
	return err
}

// MarkEnded — файл убран: how — EndedDeleted / EndedExpired / EndedRemoved; index < 0 — все файлы; owner ""
// — у всех. «Удалил сам» сильнее уборки и не перезаписывается ею. Фильм дописывается, пока его ещё
// можно узнать (единица медиатеки удаляется следом).
func (s *Service) MarkEnded(ctx context.Context, hash string, index int, how, owner string) error {
	now := s.now().UnixMilli()
	if kp := s.film(ctx, hash); kp > 0 {
		if _, err := s.db.W.ExecContext(ctx, `UPDATE view_log SET kp_id = ? WHERE hash = ? AND kp_id = 0`, kp, hash); err != nil {
			return err
		}
	}
	_, err := s.db.W.ExecContext(ctx, `UPDATE view_log SET ended = ?1, ended_at = ?2
		WHERE hash = ?3 AND (?4 < 0 OR file_index = ?4) AND (?5 = '' OR owner = ?5) AND (ended != 'deleted' OR ?1 = 'deleted')`,
		how, now, hash, index, owner)
	return err
}

// View — запись журнала: что человек смотрел и чем кончилось.
type View struct {
	Hash     string    `json:"hash"`
	Index    int       `json:"index"`
	KP       int       `json:"kpId"`
	FirstAt  time.Time `json:"firstAt"`
	LastAt   time.Time `json:"lastAt"`
	Sessions int       `json:"sessions"`
	Seen     float64   `json:"seen"`
	Max      float64   `json:"maxFraction"`
	Last     float64   `json:"lastFraction"`
	Finished bool      `json:"finished"`
	Times    int       `json:"times"`
	Ended    string    `json:"ended"`
	EndedAt  time.Time `json:"endedAt"`
	Duration float64   `json:"durationSec"` // 0 — неизвестна
}

// Views — журнал человека, новые первыми.
func (s *Service) Views(ctx context.Context, owner string) ([]View, error) {
	rows, err := s.db.R.QueryContext(ctx, `SELECT v.hash, v.file_index, v.kp_id, v.first_at, v.last_at, v.sessions, v.seen, v.max_fraction,
		v.last_fraction, v.finished, v.times, v.ended, v.ended_at, COALESCE(d.duration_sec, 0)
		FROM view_log v LEFT JOIN media_durations d ON d.hash = v.hash AND d.file_index = v.file_index
		WHERE v.owner = ? ORDER BY v.last_at DESC`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []View
	for rows.Next() {
		var v View
		var first, last, ended int64
		if err := rows.Scan(&v.Hash, &v.Index, &v.KP, &first, &last, &v.Sessions, &v.Seen, &v.Max, &v.Last, &v.Finished, &v.Times, &v.Ended, &ended, &v.Duration); err != nil {
			return nil, err
		}
		v.FirstAt, v.LastAt = time.UnixMilli(first), time.UnixMilli(last)
		if ended > 0 {
			v.EndedAt = time.UnixMilli(ended)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ResolveFilms дописывает фильмы записям, у которых их ещё нет (раздачу опознали позже первого просмотра).
func (s *Service) ResolveFilms(ctx context.Context) error {
	rows, err := s.db.R.QueryContext(ctx, `SELECT DISTINCT hash FROM view_log WHERE kp_id = 0`)
	if err != nil {
		return err
	}
	var hashes []string
	for rows.Next() {
		var h string
		rows.Scan(&h)
		hashes = append(hashes, h)
	}
	rows.Close()
	for _, h := range hashes {
		if kp := s.film(ctx, h); kp > 0 {
			if _, err := s.db.W.ExecContext(ctx, `UPDATE view_log SET kp_id = ? WHERE hash = ? AND kp_id = 0`, kp, h); err != nil {
				return err
			}
		}
	}
	return nil
}
