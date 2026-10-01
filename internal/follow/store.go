package follow

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"kinodom/internal/store"
)

// Состояния подписки (спека 11b, 6.4).
const (
	StateActive   = "active"   // проверяется раз в 6 часов
	StateFinished = "finished" // вышла и скачана последняя серия
	StateRemoved  = "removed"  // раздача снята с трекера
)

// Виды оповещений.
const (
	KindEpisodes = "episodes" // вышли новые серии
	KindRemoved  = "removed"  // раздача снята с трекера
)

// Follow — подписка на раздачу.
type Follow struct {
	Release   int64
	State     string
	InfoHash  string   // версия, которую видели последней
	Episodes  int      // вышло серий
	Total     int      // «из N»; 0 — неизвестно
	Paths     []string // видеофайлы этой версии (пути внутри раздачи); пусто — ещё не знаем
	CheckedAt time.Time
}

// Update — оповещение «Новые серии».
type Update struct {
	ID        int64
	Release   int64
	Kind      string
	InfoHash  string
	Label     string // «1×07–1×08»
	Files     []UpdateFile
	At        time.Time
	Dismissed bool
}

// UpdateFile — новая серия: номер и путь файла в раздаче (без корня).
type UpdateFile struct {
	Index int    `json:"index"`
	Path  string `json:"path"`
}

// followDB — таблицы follows и updates (миграция 0015); другие модули их не трогают, кроме RekeyTx.
type followDB struct{ db *store.DB }

func ms(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func fromMS(v int64) time.Time {
	if v == 0 {
		return time.Time{}
	}
	return time.UnixMilli(v)
}

// follow — подписаться; уже подписаны (в том числе закончившаяся или снятая) — снова активна, версия —
// нынешняя.
func (d followDB) follow(ctx context.Context, f Follow, now time.Time) error {
	paths, err := json.Marshal(nonNilPaths(f.Paths))
	if err != nil {
		return err
	}
	_, err = d.db.W.ExecContext(ctx,
		`INSERT INTO follows(release_id, state, infohash, episodes, total, paths, created_at) VALUES(?, 'active', ?, ?, ?, ?, ?)
		 ON CONFLICT(release_id) DO UPDATE SET state = 'active', infohash = excluded.infohash,
		   episodes = excluded.episodes, total = excluded.total, paths = excluded.paths`,
		f.Release, f.InfoHash, f.Episodes, f.Total, string(paths), ms(now))
	return err
}

func nonNilPaths(ps []string) []string {
	if ps == nil {
		return []string{}
	}
	return ps
}

func (d followDB) unfollow(ctx context.Context, release int64) error {
	_, err := d.db.W.ExecContext(ctx, `DELETE FROM follows WHERE release_id = ?`, release)
	return err
}

const followColumns = `release_id, state, infohash, episodes, total, paths, checked_at`

func scanFollow(sc interface{ Scan(...any) error }) (Follow, error) {
	var f Follow
	var checked int64
	var paths string
	if err := sc.Scan(&f.Release, &f.State, &f.InfoHash, &f.Episodes, &f.Total, &paths, &checked); err != nil {
		return f, err
	}
	f.CheckedAt = fromMS(checked)
	return f, json.Unmarshal([]byte(paths), &f.Paths)
}

// get — подписка на раздачу; ok = false — не следят.
func (d followDB) get(ctx context.Context, release int64) (Follow, bool, error) {
	f, err := scanFollow(d.db.R.QueryRowContext(ctx, `SELECT `+followColumns+` FROM follows WHERE release_id = ?`, release))
	if errors.Is(err, sql.ErrNoRows) {
		return Follow{}, false, nil
	}
	return f, err == nil, err
}

// active — подписки, которые проверяются.
func (d followDB) active(ctx context.Context) ([]Follow, error) {
	rows, err := d.db.R.QueryContext(ctx, `SELECT `+followColumns+` FROM follows WHERE state = 'active' ORDER BY release_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Follow
	for rows.Next() {
		f, err := scanFollow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func (d followDB) setChecked(ctx context.Context, release int64, at time.Time) error {
	_, err := d.db.W.ExecContext(ctx, `UPDATE follows SET checked_at = ? WHERE release_id = ?`, ms(at), release)
	return err
}

// setVersion — версия раздачи учтена: её infohash, сколько в ней серий и её видеофайлы.
func (d followDB) setVersion(ctx context.Context, release int64, infohash string, episodes, total int, paths []string) error {
	b, err := json.Marshal(nonNilPaths(paths))
	if err != nil {
		return err
	}
	_, err = d.db.W.ExecContext(ctx, `UPDATE follows SET infohash = ?, episodes = ?, total = ?, paths = ? WHERE release_id = ?`,
		infohash, episodes, total, string(b), release)
	return err
}

func (d followDB) setState(ctx context.Context, release int64, state string) error {
	_, err := d.db.W.ExecContext(ctx, `UPDATE follows SET state = ? WHERE release_id = ?`, state, release)
	return err
}

func (d followDB) addUpdate(ctx context.Context, u Update) (int64, error) {
	files, err := json.Marshal(nonNil(u.Files))
	if err != nil {
		return 0, err
	}
	res, err := d.db.W.ExecContext(ctx, `INSERT INTO updates(release_id, kind, infohash, files, label, at) VALUES(?, ?, ?, ?, ?, ?)`,
		u.Release, u.Kind, u.InfoHash, string(files), u.Label, ms(u.At))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func nonNil(fs []UpdateFile) []UpdateFile {
	if fs == nil {
		return []UpdateFile{}
	}
	return fs
}

// updates — оповещения, которые не убрали, новые сверху.
func (d followDB) updates(ctx context.Context) ([]Update, error) {
	rows, err := d.db.R.QueryContext(ctx,
		`SELECT id, release_id, kind, infohash, files, label, at FROM updates WHERE dismissed = 0 ORDER BY at DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Update
	for rows.Next() {
		var u Update
		var files string
		var at int64
		if err := rows.Scan(&u.ID, &u.Release, &u.Kind, &u.InfoHash, &files, &u.Label, &at); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(files), &u.Files); err != nil {
			return nil, err
		}
		u.At = fromMS(at)
		out = append(out, u)
	}
	return out, rows.Err()
}

func (d followDB) dismiss(ctx context.Context, id int64) error {
	_, err := d.db.W.ExecContext(ctx, `UPDATE updates SET dismissed = 1 WHERE id = ?`, id)
	return err
}

// RekeyTx — переход скачанной раздачи на новую версию (спека 11b, 6.3.4), внутри транзакции перехода:
// известный infohash подписок и оповещений — новый, номера файлов оповещений — по index (старый → новый);
// файл без пары из оповещения убирается.
func RekeyTx(ctx context.Context, tx *sql.Tx, old, new string, index map[int]int) error {
	if _, err := tx.ExecContext(ctx, `UPDATE follows SET infohash = ? WHERE infohash = ?`, new, old); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, files FROM updates WHERE infohash = ?`, old)
	if err != nil {
		return err
	}
	type change struct {
		id    int64
		files string
	}
	var changes []change
	for rows.Next() {
		var id int64
		var files string
		if err := rows.Scan(&id, &files); err != nil {
			rows.Close()
			return err
		}
		var fs []UpdateFile
		if err := json.Unmarshal([]byte(files), &fs); err != nil {
			rows.Close()
			return err
		}
		var kept []UpdateFile
		for _, f := range fs {
			if i, ok := index[f.Index]; ok {
				kept = append(kept, UpdateFile{i, f.Path})
			}
		}
		b, err := json.Marshal(nonNil(kept))
		if err != nil {
			rows.Close()
			return err
		}
		changes = append(changes, change{id, string(b)})
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, c := range changes {
		if _, err := tx.ExecContext(ctx, `UPDATE updates SET infohash = ?, files = ? WHERE id = ?`, new, c.files, c.id); err != nil {
			return err
		}
	}
	return nil
}
