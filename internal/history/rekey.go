package history

import (
	"context"
	"database/sql"
)

// RekeyTx — переход скачанной раздачи на обновлённую версию (спека 11b, 6.3.4), внутри транзакции
// перехода: места и отметки всех устройств и длительности — (old, i) → (new, index[i]). Строка новой
// версии уже есть (её открывали) — объединяются: «просмотрено» по «или», место — свежее. Номера без
// пары — удаляются вместе с остальным прежним.
func RekeyTx(ctx context.Context, tx *sql.Tx, old, new string, index map[int]int) error {
	for i, j := range index {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO watch_progress (device, hash, file_index, fraction, position_sec, duration_sec, watched, updated_at)
			 SELECT device, ?, ?, fraction, position_sec, duration_sec, watched, updated_at FROM watch_progress
			 WHERE hash = ? AND file_index = ?
			 ON CONFLICT(device, hash, file_index) DO UPDATE SET
			   fraction = CASE WHEN excluded.updated_at > watch_progress.updated_at THEN excluded.fraction ELSE watch_progress.fraction END,
			   position_sec = CASE WHEN excluded.updated_at > watch_progress.updated_at THEN excluded.position_sec ELSE watch_progress.position_sec END,
			   duration_sec = CASE WHEN excluded.updated_at > watch_progress.updated_at THEN excluded.duration_sec ELSE watch_progress.duration_sec END,
			   watched = watch_progress.watched OR excluded.watched,
			   updated_at = MAX(watch_progress.updated_at, excluded.updated_at)`,
			new, j, old, i); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO media_durations (hash, file_index, duration_sec)
			 SELECT ?, ?, duration_sec FROM media_durations WHERE hash = ? AND file_index = ?`, new, j, old, i); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM watch_progress WHERE hash = ?`, old); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `DELETE FROM media_durations WHERE hash = ?`, old)
	return err
}

// WatchedAny — файл просмотрен хоть на одном устройстве: оповещение «Новые серии» уходит, когда новую
// серию досмотрели где угодно (спека 11b, 6.1).
func (s *Service) WatchedAny(ctx context.Context, hash string, index int) (bool, error) {
	var ok bool
	err := s.db.R.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM watch_progress WHERE hash = ? AND file_index = ? AND watched = 1)`, hash, index).Scan(&ok)
	return ok, err
}
