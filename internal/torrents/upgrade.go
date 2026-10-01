package torrents

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
)

// Переход скачанной раздачи на обновлённую версию (спека 11b, 6.3; опыт — исследование 22.1): раздачу
// сериала обновили на трекере (новый infohash: прежние серии и новая), скачанное переносится в папку новой
// версии и проверяется — докачивается только новое.

// Rekey — перенос ключей (infohash, номер файла) в таблицах других модулей (история просмотров,
// медиатека, подписки) внутри транзакции перехода; index — номер файла прежней версии → новой.
type Rekey func(ctx context.Context, tx *sql.Tx, old, new metainfo.Hash, index map[int]int) error

// ErrBusy — раздачу сейчас смотрят: Windows не даёт переименовать открытый файл, переход — позже.
var ErrBusy = errors.New("раздачу сейчас смотрят — переход позже")

// busyAfterStream — поток был недавно: VLC переподключается каждые 2 с, между соединениями потоков нет.
const busyAfterStream = 2 * time.Minute

// upgradeMove — файл того же размера, перенесённый в папку новой версии.
type upgradeMove struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Index int    `json:"index"` // номер файла в новой версии
}

// upgradeRec — пометка «идёт переход» (таблица upgrades): по ней переход доводится после сбоя.
type upgradeRec struct {
	Old, New metainfo.Hash `json:"-"`
	State    string        `json:"-"`     // db — база переписана, файлы переносятся; moved — перенесены, ждут проверки
	Dir      string        `json:"dir"`   // папка загрузок раздачи
	Root     string        `json:"root"`  // папка прежней версии — удаляется после переноса
	Focus    int           `json:"focus"` // файл в очереди загрузки (номер в новой версии); −1 — никакой
	Moves    []upgradeMove `json:"moves"`
	Download []int         `json:"download"` // новые серии и перезалитые — в очередь после перехода (и после сбоя)
}

// isUpgrading — переход раздачи ih (прежней или новой версии) идёт в этом процессе.
func (s *Service) isUpgrading(ih metainfo.Hash) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.upgrading[ih]
}

// SetRekey — перенос ключей для перехода, который доводится при старте (Upgrade получает свой).
func (s *Service) SetRekey(r Rekey) {
	s.mu.Lock()
	s.rekey = r
	s.mu.Unlock()
}

// Upgrade — переход скачанной раздачи old на новую версию newRaw (.torrent с info). Файлы того же
// размера переносятся (сопоставление — matchFiles), остальные старые удаляются вместе с папкой прежней
// версии; файлы, что были скачаны, но в новой версии другого размера (перезалиты), и download (новые
// серии) встают в очередь после проверки перенесённого. Смотрят (поток открыт или был меньше
// busyAfterStream назад, идёт перепроверка) — ErrBusy, ничего не меняется. Возвращает infohash новой версии;
// ошибка вместе с ним — переход записан (ключи перенесены), а файлы не перенеслись: его доведёт уборка.
// Докачка новых серий не встала (мало места) — не ошибка перехода: только в журнал.
func (s *Service) Upgrade(ctx context.Context, old metainfo.Hash, newRaw []byte, download []int, rekey Rekey) (metainfo.Hash, error) {
	mi, err := metainfo.Load(bytes.NewReader(newRaw))
	if err != nil {
		return metainfo.Hash{}, fmt.Errorf("метаинфо новой версии не читается: %w", err)
	}
	info, err := mi.UnmarshalInfo()
	if err != nil {
		return metainfo.Hash{}, fmt.Errorf("метаинфо новой версии не читается: %w", err)
	}
	newIH := mi.HashInfoBytes()
	if newIH == old {
		return old, nil
	}
	if rekey == nil {
		s.mu.Lock()
		rekey = s.rekey
		s.mu.Unlock()
	}
	last, err := s.reg.lastStream(ctx, old)
	if err != nil {
		return metainfo.Hash{}, err
	}
	if s.now().Sub(last) < busyAfterStream {
		return metainfo.Hash{}, ErrBusy
	}
	s.spaceMu.Lock()
	s.mu.Lock()
	ss := s.sessions[old]
	if ss == nil || ss.t.Info() == nil {
		s.mu.Unlock()
		s.spaceMu.Unlock()
		return metainfo.Hash{}, fmt.Errorf("раздачи %s нет в движке", old.HexString())
	}
	if ss.streaming() || len(ss.verifyQ) > 0 || s.verifyNow.ih == old {
		s.mu.Unlock()
		s.spaceMu.Unlock()
		return metainfo.Hash{}, ErrBusy
	}
	if s.upgrading == nil {
		s.upgrading = map[metainfo.Hash]bool{}
	}
	s.upgrading[old], s.upgrading[newIH] = true, true
	defer func() {
		s.mu.Lock()
		delete(s.upgrading, old)
		delete(s.upgrading, newIH)
		s.mu.Unlock()
	}()
	oldInfo := ss.t.Info()
	dir := s.eng.TorrentDir(old)
	index := matchFiles(oldInfo, &info)
	oldFiles, newFiles := oldInfo.UpvertedFiles(), info.UpvertedFiles()
	rec := upgradeRec{Old: old, New: newIH, State: "db", Dir: dir, Root: torrentDir(dir, oldInfo, old), Focus: -1}
	var again []int // скачанные, но перезалитые (другой размер) — качать заново
	for i := range ss.storedFiles {
		j, ok := index[i]
		switch {
		case !ok:
		case oldFiles[i].Length == newFiles[j].Length:
			rec.Moves = append(rec.Moves, upgradeMove{From: enginePath(dir, oldInfo, old, oldFiles[i]), To: enginePath(dir, &info, newIH, newFiles[j]), Index: j})
		default:
			again = append(again, j)
		}
	}
	if j, ok := index[ss.focus]; ok {
		rec.Focus = j
	}
	rec.Download = append(again, download...)
	err = s.reg.upgrade(ctx, rec, info.BestName(), newRaw, s.now(), func(tx *sql.Tx) error {
		if rekey == nil {
			return nil
		}
		return rekey(ctx, tx, old, newIH, index)
	})
	if err != nil {
		s.mu.Unlock()
		s.spaceMu.Unlock()
		return metainfo.Hash{}, err
	}
	// Прежняя версия — из движка (файлы остаются на диске: их переносим).
	ss.t.Drop()
	select {
	case <-ss.t.Closed():
	case <-time.After(5 * time.Second):
	}
	delete(s.sessions, old)
	s.mu.Unlock()
	err = s.finishUpgrade(ctx, rec, mi, false)
	s.spaceMu.Unlock()
	if err != nil {
		return newIH, err
	}
	s.queueAfterUpgrade(ctx, rec)
	return newIH, nil
}

// queueAfterUpgrade — новые серии и перезалитые файлы — в очередь загрузки. Не встали (мало места) —
// переход всё равно состоялся: «Смотреть» скачает серию потоком, а место — забота правил «Загрузок».
func (s *Service) queueAfterUpgrade(ctx context.Context, rec upgradeRec) {
	if len(rec.Download) == 0 {
		return
	}
	if err := s.Download(ctx, rec.New, rec.Download); err != nil {
		s.log.Warn("новые серии после перехода не встали в очередь", "hash", rec.New.HexString(), "err", err)
	}
}

// finishUpgrade — вторая половина перехода (и доведение после сбоя): перенести файлы, убрать папку
// прежней версии, добавить новую версию в движок и проверить перенесённое (verifyAll — все хранимые
// файлы: после сбоя не известно, что успели). Пометка «идёт переход» снимается после проверки.
func (s *Service) finishUpgrade(ctx context.Context, rec upgradeRec, mi *metainfo.MetaInfo, verifyAll bool) error {
	if rec.State == "db" {
		if err := s.stopAt("db", rec); err != nil {
			return err
		}
		for _, m := range rec.Moves {
			if _, err := os.Stat(m.From); err != nil {
				continue // уже перенесён (сбой посередине) или пропал — тогда докачается
			}
			if err := os.MkdirAll(filepath.Dir(m.To), 0o755); err != nil {
				return err
			}
			os.Remove(m.To) // заготовка движка, если новую версию уже открывали
			if err := retry(func() error { return os.Rename(m.From, m.To) }); err != nil {
				return fmt.Errorf("файл не переносится в папку новой версии: %w", err)
			}
		}
		if err := removeAllRetry(rec.Root); err != nil {
			s.log.Warn("папка прежней версии раздачи не удалилась", "dir", rec.Root, "err", err)
		}
		if err := s.reg.setUpgradeState(ctx, rec.Old, "moved"); err != nil {
			return err
		}
		rec.State = "moved"
	}
	if err := s.stopAt("moved", rec); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.eng.cl.Torrent(rec.New)
	if !ok {
		s.eng.SetTorrentDir(rec.New, rec.Dir)
		var err error
		if t, err = s.eng.cl.AddTorrent(mi); err != nil {
			return fmt.Errorf("новая версия раздачи не добавилась: %w", err)
		}
	}
	idxs, err := s.reg.StoredFiles(ctx, rec.New)
	if err != nil {
		return err
	}
	ss := s.sessionFor(t)
	ss.stored, ss.metaSaved = true, true
	ss.lastSeen = s.now()
	files := t.Files()
	for _, i := range idxs {
		if i >= 0 && i < len(files) {
			ss.storedFiles[i] = true
		}
	}
	ss.focus = rec.Focus
	if verifyAll {
		s.queueVerify(ss)
	} else {
		var moved []int
		for _, m := range rec.Moves {
			moved = append(moved, m.Index)
		}
		s.queueVerifyFiles(ss, moved)
	}
	ss.upgradedFrom = rec.Old
	if len(ss.verifyQ) == 0 {
		s.verifyDone(ss)
	}
	if ss.focus < 0 || !ss.storedFiles[ss.focus] {
		s.setFocusLocked(ss, s.nextFocusLocked(ss))
	}
	s.applyLocked(ss)
	return nil
}

// stopAt — точка «сбоя» для тестов доведения перехода.
func (s *Service) stopAt(phase string, rec upgradeRec) error {
	if s.upgradeStop == nil {
		return nil
	}
	return s.upgradeStop(phase, rec)
}

// resumeUpgrades — переходы, прерванные сбоем, доводятся (до восстановления раздач): при старте и уборкой,
// если в прошлый раз не вышло (диск не подключён, файл заняли). Не трогаются переход, идущий в этом
// процессе, и уже доведённый (новая версия в движке — пометка снимется после проверки): иначе уборка раз
// в 5 минут ставила бы проверку заново и гонялась бы с переносом файлов (финальное ревью 11b-В).
func (s *Service) resumeUpgrades(ctx context.Context) error {
	recs, err := s.reg.upgrades(ctx)
	if err != nil {
		return err
	}
	for _, rec := range recs {
		if s.isUpgrading(rec.Old) || s.isUpgrading(rec.New) {
			continue
		}
		if _, ok := s.eng.cl.Torrent(rec.New); ok {
			continue
		}
		if _, err := os.Stat(rec.Dir); err != nil {
			continue // диск не подключён: «уже перенесён» по пропавшему файлу было бы неправдой
		}
		raw, ok, err := s.reg.Metainfo(ctx, rec.New)
		if err != nil {
			return err
		}
		if !ok {
			s.log.Warn("переход раздачи не доводится: нет метаинфо новой версии", "hash", rec.New.HexString())
			continue
		}
		mi, err := metainfo.Load(bytes.NewReader(raw))
		if err != nil {
			return err
		}
		if err := s.finishUpgrade(ctx, rec, mi, true); err != nil {
			s.log.Warn("переход раздачи не доведён — повтор при уборке", "hash", rec.New.HexString(), "err", err)
			continue
		}
		s.queueAfterUpgrade(ctx, rec)
	}
	return nil
}

// queueVerifyFiles — как queueVerify, но только файлы files (перенесённые при переходе). Вызывать под s.mu.
func (s *Service) queueVerifyFiles(ss *session, files []int) {
	t := ss.t
	fs := t.Files()
	for _, i := range files {
		if i < 0 || i >= len(fs) {
			continue
		}
		fs[i].SetPriority(torrent.PiecePriorityNone)
		for p := fs[i].BeginPieceIndex(); p < fs[i].EndPieceIndex(); p++ {
			if !ss.verifyQ[p] {
				ss.verifyQ[p] = true
				s.toVerify = append(s.toVerify, pieceRef{t.InfoHash(), p})
			}
		}
	}
}

// matchFiles — файлы прежней версии в новой: по пути внутри раздачи (без корня — его переименовывают),
// а не найденные — по размеру, если он единственный среди не найденных в обеих версиях (файлы
// переименовали). Номер файла — позиция в метаинфо: вставка сдвигает номера, поэтому только так.
func matchFiles(old, new *metainfo.Info) map[int]int {
	key := func(f metainfo.FileInfo) string { return strings.Join(f.BestPath(), "/") }
	oldFiles, newFiles := old.UpvertedFiles(), new.UpvertedFiles()
	byPath := map[string]int{}
	for j, f := range newFiles {
		byPath[key(f)] = j
	}
	out := map[int]int{}
	used := map[int]bool{}
	for i, f := range oldFiles {
		if j, ok := byPath[key(f)]; ok && !used[j] {
			out[i], used[j] = j, true
		}
	}
	oldBySize, newBySize := map[int64][]int{}, map[int64][]int{}
	for i, f := range oldFiles {
		if _, ok := out[i]; !ok {
			oldBySize[f.Length] = append(oldBySize[f.Length], i)
		}
	}
	for j, f := range newFiles {
		if !used[j] {
			newBySize[f.Length] = append(newBySize[f.Length], j)
		}
	}
	for size, is := range oldBySize {
		if js := newBySize[size]; len(is) == 1 && len(js) == 1 {
			out[is[0]] = js[0]
		}
	}
	return out
}

// upgrade — одна транзакция перехода: новая версия в реестре (папка, «качать всё» и очередь — от
// прежней), перенесённые файлы — хранимыми в новой, ключи в чужих таблицах (rekey), прежняя версия
// удаляется, пометка «идёт переход».
func (r *Registry) upgrade(ctx context.Context, rec upgradeRec, name string, raw []byte, now time.Time, rekey func(*sql.Tx) error) error {
	moves, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	tx, err := r.db.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	old, new := rec.Old.HexString(), rec.New.HexString()
	res, err := tx.ExecContext(ctx,
		`INSERT INTO torrents(infohash, name, metainfo, source, added_at, dir, download_all, focus_file)
		 SELECT ?, ?, ?, 'torrent-file', added_at, dir, download_all, ? FROM torrents WHERE infohash = ?
		 ON CONFLICT(infohash) DO UPDATE SET name = excluded.name, metainfo = excluded.metainfo, dir = excluded.dir,
		   download_all = excluded.download_all, focus_file = excluded.focus_file`,
		new, name, raw, rec.Focus, old)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("раздачи %s нет в реестре", old)
	}
	for _, m := range rec.Moves {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO stored_files(infohash, file_index, path, size, last_opened_at, last_stream_at)
			 SELECT ?, ?, ?, size, last_opened_at, last_stream_at FROM stored_files WHERE infohash = ? AND path = ?
			 ON CONFLICT(infohash, file_index) DO UPDATE SET path = excluded.path`,
			new, m.Index, m.To, old, m.From); err != nil {
			return err
		}
	}
	if err := rekey(tx); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM torrents WHERE infohash = ?`, old); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO upgrades(old, new, moves, state, started_at) VALUES(?, ?, ?, 'db', ?)`,
		old, new, string(moves), now.UnixMilli()); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Registry) setUpgradeState(ctx context.Context, old metainfo.Hash, state string) error {
	_, err := r.db.W.ExecContext(ctx, `UPDATE upgrades SET state = ? WHERE old = ?`, state, old.HexString())
	return err
}

// endUpgrade — переход доведён и проверен: пометка снимается.
func (r *Registry) endUpgrade(ctx context.Context, old metainfo.Hash) error {
	_, err := r.db.W.ExecContext(ctx, `DELETE FROM upgrades WHERE old = ?`, old.HexString())
	return err
}

// upgrades — переходы, которые не доведены.
func (r *Registry) upgrades(ctx context.Context) ([]upgradeRec, error) {
	rows, err := r.db.R.QueryContext(ctx, `SELECT old, new, moves, state FROM upgrades ORDER BY started_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []upgradeRec
	for rows.Next() {
		var old, new, moves, state string
		if err := rows.Scan(&old, &new, &moves, &state); err != nil {
			return nil, err
		}
		var rec upgradeRec
		if err := json.Unmarshal([]byte(moves), &rec); err != nil {
			return nil, err
		}
		if err := rec.Old.FromHexString(old); err != nil {
			return nil, err
		}
		if err := rec.New.FromHexString(new); err != nil {
			return nil, err
		}
		rec.State = state
		out = append(out, rec)
	}
	return out, rows.Err()
}

// lastStream — когда раздачу смотрели последний раз (по всем файлам); нуль — ни разу.
func (r *Registry) lastStream(ctx context.Context, ih metainfo.Hash) (time.Time, error) {
	var ms int64
	err := r.db.R.QueryRowContext(ctx, `SELECT COALESCE(MAX(last_stream_at), 0) FROM stored_files WHERE infohash = ?`,
		ih.HexString()).Scan(&ms)
	if err != nil || ms == 0 {
		return time.Time{}, err
	}
	return time.UnixMilli(ms), nil
}
