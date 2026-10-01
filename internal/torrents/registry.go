package torrents

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/anacrolix/torrent/bencode"
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
	Focus    int    // файл, который качался (очередь загрузки); −1 — никакой
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

// PinDirs закрепляет папку за раздачами без папки (записаны до этапа 6): они лежат в папке
// загрузок, которая действует сейчас. Иначе после смены папки в пульте их искали бы в новой.
// Hashes — все раздачи реестра: чьи отметки кусков нужны движку (остальные удаляются при старте).
func (r *Registry) Hashes(ctx context.Context) ([]metainfo.Hash, error) {
	rows, err := r.db.R.QueryContext(ctx, `SELECT infohash FROM torrents`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []metainfo.Hash
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		var ih metainfo.Hash
		if err := ih.FromHexString(s); err != nil {
			continue
		}
		out = append(out, ih)
	}
	return out, rows.Err()
}

func (r *Registry) PinDirs(ctx context.Context, dir string) error {
	_, err := r.db.W.ExecContext(ctx, `UPDATE torrents SET dir = ? WHERE dir = ''`, dir)
	return err
}

// SaveMetainfo сохраняет метаинфо, когда движок её получил. Источник не меняется.
func (r *Registry) SaveMetainfo(ctx context.Context, ih metainfo.Hash, name string, mi []byte) error {
	_, err := r.db.W.ExecContext(ctx,
		`INSERT INTO torrents(infohash, name, metainfo, added_at) VALUES(?, ?, ?, ?)
		 ON CONFLICT(infohash) DO UPDATE SET name = excluded.name, metainfo = excluded.metainfo`,
		ih.HexString(), name, mi, time.Now().UnixMilli())
	return err
}

// MarkStored — файл выбран («Смотреть» или «Скачать»): он хранится и докачивается целиком. Выбор —
// не открытие (спека этапа 9, раздел 5.8): у нового файла время открытия — «никогда», открытием
// считается поток (TouchStream).
func (r *Registry) MarkStored(ctx context.Context, ih metainfo.Hash, index int, path string, size int64) error {
	_, err := r.db.W.ExecContext(ctx,
		`INSERT INTO stored_files(infohash, file_index, path, size, last_opened_at) VALUES(?, ?, ?, ?, 0)
		 ON CONFLICT(infohash, file_index) DO UPDATE SET path = excluded.path`,
		ih.HexString(), index, path, size)
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
		`SELECT t.infohash, t.name, t.metainfo, t.source, t.dir, t.focus_file FROM torrents t
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
		if err := rows.Scan(&hexHash, &rec.Name, &rec.Metainfo, &rec.Source, &rec.Dir, &rec.Focus); err != nil {
			return nil, err
		}
		if err := rec.InfoHash.FromHexString(hexHash); err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// Folders — папки всех раздач с метаинфо на диске: у раздачи со своей папкой загрузок — в ней, у
// раздачи без папки — в defaultDir. Для удаления Kinodom «со скачанным» (этап 11a): удаляются
// только папки раздач, чужие файлы в папке загрузок остаются.
func (r *Registry) Folders(ctx context.Context, defaultDir string) ([]string, error) {
	rows, err := r.db.R.QueryContext(ctx, `SELECT infohash, metainfo, dir FROM torrents WHERE metainfo IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var hexHash, dir string
		var mi []byte
		if err := rows.Scan(&hexHash, &mi, &dir); err != nil {
			return nil, err
		}
		var ih metainfo.Hash
		if err := ih.FromHexString(hexHash); err != nil {
			return nil, err
		}
		var info metainfo.Info
		if err := bencode.Unmarshal(mi, &info); err != nil {
			continue // испорченная метаинфо: папку не вычислить, раздача и не восстановится
		}
		if dir == "" {
			dir = defaultDir
		}
		out = append(out, torrentDir(dir, &info, ih))
	}
	return out, rows.Err()
}

// Pending — раздачи, для которых нажали «Скачать» до того, как пришёл список файлов: после
// перезапуска их надо открыть снова.
func (r *Registry) Pending(ctx context.Context) ([]Record, error) {
	rows, err := r.db.R.QueryContext(ctx,
		`SELECT infohash, name, metainfo, source, dir FROM torrents WHERE download_all = 1 ORDER BY added_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Record
	for rows.Next() {
		rec := Record{Focus: -1}
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

// SetDownloadAll запоминает (или снимает) «Скачать всё» до получения списка файлов.
func (r *Registry) SetDownloadAll(ctx context.Context, ih metainfo.Hash, on bool) error {
	_, err := r.db.W.ExecContext(ctx, `UPDATE torrents SET download_all = ? WHERE infohash = ?`, on, ih.HexString())
	return err
}

// SetFocus запоминает файл, который качается сейчас: после перезапуска очередь продолжится с него.
func (r *Registry) SetFocus(ctx context.Context, ih metainfo.Hash, index int) error {
	_, err := r.db.W.ExecContext(ctx, `UPDATE torrents SET focus_file = ? WHERE infohash = ?`, index, ih.HexString())
	return err
}

// Metainfo — сохранённая метаинфо раздачи; false — раздачу не открывали или метаданных ещё нет.
func (r *Registry) Metainfo(ctx context.Context, ih metainfo.Hash) ([]byte, bool, error) {
	var b []byte
	err := r.db.R.QueryRowContext(ctx, `SELECT metainfo FROM torrents WHERE infohash = ? AND metainfo IS NOT NULL`,
		ih.HexString()).Scan(&b)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	return b, err == nil, err
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
	if opened > 0 {
		f.LastOpened = time.UnixMilli(opened)
	}
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
		 WHERE t.added_at < ? AND t.download_all = 0
		   AND NOT EXISTS (SELECT 1 FROM stored_files f WHERE f.infohash = t.infohash)`,
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

// TorrentsByOpened — раздачи с хранимыми файлами, самые недавно открытые — первыми (раздача). Ни разу
// не открытая (скачанная заранее, спека этапа 9, раздел 5.8) — по времени добавления: только что
// скачанная раздаётся, давно лежащая — нет.
func (r *Registry) TorrentsByOpened(ctx context.Context) ([]metainfo.Hash, error) {
	rows, err := r.db.R.QueryContext(ctx,
		`SELECT s.infohash FROM stored_files s LEFT JOIN torrents t ON t.infohash = s.infohash GROUP BY s.infohash
		 ORDER BY CASE WHEN MAX(s.last_opened_at) > 0 THEN MAX(s.last_opened_at) ELSE COALESCE(MAX(t.added_at), 0) END DESC, s.infohash`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []metainfo.Hash
	for rows.Next() {
		var hexHash string
		var ih metainfo.Hash
		if err := rows.Scan(&hexHash); err != nil {
			return nil, err
		}
		if err := ih.FromHexString(hexHash); err != nil {
			return nil, err
		}
		out = append(out, ih)
	}
	return out, rows.Err()
}
