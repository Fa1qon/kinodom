package torrents

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/anacrolix/torrent/metainfo"

	"kinodom/internal/store"
)

// Registry — раздачи в базе: метаинфо (после перезапуска не надо ждать её от пиров)
// и хранимые файлы (очистка на этапе 6, раздел «Скачано» на этапе 9).
type Registry struct{ db *store.DB }

func NewRegistry(db *store.DB) *Registry { return &Registry{db: db} }

// Record — раздача, которую можно восстановить после перезапуска.
type Record struct {
	InfoHash metainfo.Hash
	Name     string
	Metainfo []byte // bencode
	Source   string
	Dir      string // папка загрузок раздачи; пусто — текущая
}

// Remember запоминает раздачу при открытии; метаинфо может ещё не быть. Возвращает папку
// загрузок раздачи: у знакомой — ту, куда она качалась, у новой — dir.
func (r *Registry) Remember(ctx context.Context, ih metainfo.Hash, source, dir string) (string, error) {
	var got string
	err := r.db.W.QueryRowContext(ctx,
		`INSERT INTO torrents(infohash, source, added_at, dir) VALUES(?, ?, ?, ?)
		 ON CONFLICT(infohash) DO UPDATE SET dir = CASE WHEN dir = '' THEN excluded.dir ELSE dir END
		 RETURNING dir`,
		ih.HexString(), source, time.Now().UnixMilli(), dir).Scan(&got)
	return got, err
}

// SaveMetainfo сохраняет метаинфо, когда движок её получил. Источник не меняется.
func (r *Registry) SaveMetainfo(ctx context.Context, ih metainfo.Hash, name string, mi []byte) error {
	_, err := r.db.W.ExecContext(ctx,
		`INSERT INTO torrents(infohash, name, metainfo, added_at) VALUES(?, ?, ?, ?)
		 ON CONFLICT(infohash) DO UPDATE SET name = excluded.name, metainfo = excluded.metainfo`,
		ih.HexString(), name, mi, time.Now().UnixMilli())
	return err
}

// MarkStored — файл выбран для просмотра: он хранится и докачивается целиком.
func (r *Registry) MarkStored(ctx context.Context, ih metainfo.Hash, index int, path string, size int64, now time.Time) error {
	_, err := r.db.W.ExecContext(ctx,
		`INSERT INTO stored_files(infohash, file_index, path, size, last_opened_at) VALUES(?, ?, ?, ?, ?)
		 ON CONFLICT(infohash, file_index) DO UPDATE SET path = excluded.path, last_opened_at = excluded.last_opened_at`,
		ih.HexString(), index, path, size, now.UnixMilli())
	return err
}

// TouchStream — к файлу подключился плеер: «сейчас смотрят». Заодно это открытие файла (срок
// хранения считается от него) — не чаще раза в минуту (спека, раздел 9).
func (r *Registry) TouchStream(ctx context.Context, ih metainfo.Hash, index int, now time.Time) error {
	_, err := r.db.W.ExecContext(ctx,
		`UPDATE stored_files SET last_stream_at = ?1,
		   last_opened_at = CASE WHEN ?1 - last_opened_at >= 60000 THEN ?1 ELSE last_opened_at END
		 WHERE infohash = ?2 AND file_index = ?3`,
		now.UnixMilli(), ih.HexString(), index)
	return err
}

// Restorable — раздачи с метаинфо и хотя бы одним хранимым файлом.
func (r *Registry) Restorable(ctx context.Context) ([]Record, error) {
	rows, err := r.db.R.QueryContext(ctx,
		`SELECT t.infohash, t.name, t.metainfo, t.source, t.dir FROM torrents t
		 WHERE t.metainfo IS NOT NULL
		   AND EXISTS (SELECT 1 FROM stored_files f WHERE f.infohash = t.infohash)
		 ORDER BY t.added_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Record
	for rows.Next() {
		var rec Record
		var hexHash string
		if err := rows.Scan(&hexHash, &rec.Name, &rec.Metainfo, &rec.Source, &rec.Dir); err != nil {
			return nil, err
		}
		if err := rec.InfoHash.FromHexString(hexHash); err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// StoredFiles — номера хранимых файлов раздачи.
func (r *Registry) StoredFiles(ctx context.Context, ih metainfo.Hash) ([]int, error) {
	rows, err := r.db.R.QueryContext(ctx,
		`SELECT file_index FROM stored_files WHERE infohash = ? ORDER BY file_index`, ih.HexString())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int
	for rows.Next() {
		var i int
		if err := rows.Scan(&i); err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

func (r *Registry) setProblem(ctx context.Context, id, text string) {
	r.db.SetProblem(context.WithoutCancel(ctx), id, text)
}

func (r *Registry) clearProblem(ctx context.Context, id string) {
	r.db.ClearProblem(context.WithoutCancel(ctx), id)
}

// StoredFile — хранимый файл: когда его открывали и смотрели.
type StoredFile struct {
	InfoHash   metainfo.Hash
	Index      int
	Path       string
	Size       int64
	LastOpened time.Time
	LastStream time.Time // ноль — не смотрели
}

const storedFileColumns = `infohash, file_index, path, size, last_opened_at, last_stream_at`

func scanStoredFile(sc interface{ Scan(...any) error }) (StoredFile, error) {
	var (
		f              StoredFile
		hexHash        string
		opened, stream int64
	)
	if err := sc.Scan(&hexHash, &f.Index, &f.Path, &f.Size, &opened, &stream); err != nil {
		return f, err
	}
	if err := f.InfoHash.FromHexString(hexHash); err != nil {
		return f, err
	}
	f.LastOpened = time.UnixMilli(opened)
	if stream > 0 {
		f.LastStream = time.UnixMilli(stream)
	}
	return f, nil
}

// StoredFile — хранимый файл раздачи; false — такого нет.
func (r *Registry) StoredFile(ctx context.Context, ih metainfo.Hash, index int) (StoredFile, bool, error) {
	f, err := scanStoredFile(r.db.R.QueryRowContext(ctx,
		`SELECT `+storedFileColumns+` FROM stored_files WHERE infohash = ? AND file_index = ?`, ih.HexString(), index))
	if errors.Is(err, sql.ErrNoRows) {
		return f, false, nil
	}
	return f, err == nil, err
}

// Unstore — файл удалён с диска: больше не хранится и не докачивается.
func (r *Registry) Unstore(ctx context.Context, ih metainfo.Hash, index int) error {
	_, err := r.db.W.ExecContext(ctx, `DELETE FROM stored_files WHERE infohash = ? AND file_index = ?`, ih.HexString(), index)
	return err
}

// Forget — раздача убрана совсем (её хранимые файлы уходят вместе с ней).
func (r *Registry) Forget(ctx context.Context, ih metainfo.Hash) error {
	_, err := r.db.W.ExecContext(ctx, `DELETE FROM torrents WHERE infohash = ?`, ih.HexString())
	return err
}

// StoredByAge — хранимые файлы, самые давно открытые — первыми (очистка).
func (r *Registry) StoredByAge(ctx context.Context) ([]StoredFile, error) {
	rows, err := r.db.R.QueryContext(ctx,
		`SELECT `+storedFileColumns+` FROM stored_files ORDER BY last_opened_at, infohash, file_index`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StoredFile
	for rows.Next() {
		f, err := scanStoredFile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// Unstored — раздачи без хранимых файлов, открытые раньше before (уборка).
func (r *Registry) Unstored(ctx context.Context, before time.Time) ([]Record, error) {
	rows, err := r.db.R.QueryContext(ctx,
		`SELECT t.infohash, t.name, t.metainfo, t.source, t.dir FROM torrents t
		 WHERE t.added_at < ? AND NOT EXISTS (SELECT 1 FROM stored_files f WHERE f.infohash = t.infohash)`,
		before.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Record
	for rows.Next() {
		var rec Record
		var hexHash string
		if err := rows.Scan(&hexHash, &rec.Name, &rec.Metainfo, &rec.Source, &rec.Dir); err != nil {
			return nil, err
		}
		if err := rec.InfoHash.FromHexString(hexHash); err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}
