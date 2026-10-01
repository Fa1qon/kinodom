package library

import (
	"context"
	"database/sql"
)

// RekeyTx — переход скачанной раздачи на обновлённую версию (спека 11b, 6.3.4), внутри транзакции
// перехода: единица медиатеки остаётся той же (номер, ручная привязка Кинопоиска, карточка и её
// категория), её ключ — новый infohash, номера файлов — index (старый → новый); файл без пары уходит
// (обход добавит новые). Иначе обход удалил бы единицу прежней версии вместе с ручными привязками и завёл
// новую. Единицу новой версии, которую обход успел завести раньше, — убрать (дубль без привязок).
func RekeyTx(ctx context.Context, tx *sql.Tx, old, new string, index map[int]int) error {
	var unit int64
	err := tx.QueryRowContext(ctx, `SELECT id FROM lib_units WHERE source = 'torrent' AND key = ?`, old).Scan(&unit)
	if err == sql.ErrNoRows {
		return nil // раздачи в медиатеке ещё нет — обход заведёт новую
	}
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM lib_units WHERE source = 'torrent' AND key = ?`, new); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE lib_units SET key = ? WHERE id = ?`, new, unit); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, tindex FROM lib_files WHERE unit = ?`, unit)
	if err != nil {
		return err
	}
	type file struct {
		id     int64
		tindex int
	}
	var files []file
	for rows.Next() {
		var f file
		if err := rows.Scan(&f.id, &f.tindex); err != nil {
			rows.Close()
			return err
		}
		files = append(files, f)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, f := range files {
		j, ok := index[f.tindex]
		if !ok {
			if _, err := tx.ExecContext(ctx, `DELETE FROM lib_files WHERE id = ?`, f.id); err != nil {
				return err
			}
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE lib_files SET tindex = ? WHERE id = ?`, j, f.id); err != nil {
			return err
		}
	}
	return nil
}
