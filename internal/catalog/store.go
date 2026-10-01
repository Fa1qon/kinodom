package catalog

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"kinodom/internal/source"
	"kinodom/internal/store"
)

// catalogStore — таблицы модуля catalog (миграция 0004_catalog.sql). Другие модули в них не ходят.
type catalogStore struct{ db *store.DB }

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

// row — раздача из базы.
type row struct {
	ID          int64
	Tracker     string
	TopicID     string
	Title       string
	CategoryID  string
	Seeders     int
	Leechers    int
	Size        int64
	Added       time.Time
	InfoHash    string
	ImageKey    string
	KinopoiskID int
	IMDbID      string
	DetailsAt   time.Time
	RetryAt     time.Time // страница раздачи не загрузилась — не раньше
	Format      string    // «MKV», «AVI, MKV»; "" — неизвестен или ещё не определяли
	Section     string    // раздел каталога записи (catalog_entries); у Rutracker — подраздел первого уровня, а CategoryID — форум раздачи
	Pos         int       // место записи в разделе: первая сотня — по раздающим при обновлении, порции — в конце
}

const rowColumns = `r.id, r.tracker, r.topic_id, r.title, r.category_id, r.seeders, r.leechers, r.size,
	r.added_at, r.infohash, r.image_key, r.kinopoisk_id, r.imdb_id, r.details_at, r.retry_at, COALESCE(r.format, '')`

// scanRow читает столбцы rowColumns и, после них, extra.
func scanRow(sc interface{ Scan(...any) error }, extra ...any) (row, error) {
	var r row
	var added, detailsAt, retryAt int64
	dest := append([]any{&r.ID, &r.Tracker, &r.TopicID, &r.Title, &r.CategoryID, &r.Seeders, &r.Leechers, &r.Size,
		&added, &r.InfoHash, &r.ImageKey, &r.KinopoiskID, &r.IMDbID, &detailsAt, &retryAt, &r.Format}, extra...)
	err := sc.Scan(dest...)
	r.Added, r.DetailsAt, r.RetryAt = fromMS(added), fromMS(detailsAt), fromMS(retryAt)
	return r, err
}

// upsertRelease — строка списка (топ, поиск): цифры свежие; название, раздел и infohash — только
// если пришли (у топа Rutracker названий нет). Возвращает номер раздачи.
func upsertRelease(ctx context.Context, tx *sql.Tx, r source.Release, now time.Time) (int64, error) {
	var id int64
	err := tx.QueryRowContext(ctx,
		`INSERT INTO releases(tracker, topic_id, title, category_id, seeders, leechers, size, added_at, infohash, updated_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(tracker, topic_id) DO UPDATE SET
		   title = CASE WHEN excluded.title != '' THEN excluded.title ELSE releases.title END,
		   category_id = CASE WHEN excluded.category_id != '' THEN excluded.category_id ELSE releases.category_id END,
		   seeders = excluded.seeders, leechers = excluded.leechers,
		   size = CASE WHEN excluded.size > 0 THEN excluded.size ELSE releases.size END,
		   added_at = CASE WHEN excluded.added_at > 0 THEN excluded.added_at ELSE releases.added_at END,
		   infohash = CASE WHEN excluded.infohash != '' THEN excluded.infohash ELSE releases.infohash END,
		   removed = 0, updated_at = excluded.updated_at
		 RETURNING id`,
		r.Tracker, r.TopicID, r.Title, r.CategoryID, r.Seeders, r.Leechers, r.Size, ms(r.Added), r.InfoHash, ms(now)).Scan(&id)
	return id, err
}

// replaceTop — топ раздела: раздачи обновляются, позиции раздела заменяются целиком, запоминается,
// сколько пришло и когда.
func (s catalogStore) replaceTop(ctx context.Context, cat CategoryRef, rs []source.Release, now time.Time) error {
	tx, err := s.db.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM catalog_entries WHERE tracker = ? AND category_id = ?`, cat.Tracker, cat.ID); err != nil {
		return err
	}
	for i, r := range rs {
		id, err := upsertRelease(ctx, tx, r, now)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO catalog_entries(tracker, category_id, position, release_id) VALUES(?, ?, ?, ?)`,
			cat.Tracker, cat.ID, i, id); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO catalog_state(tracker, category_id, last_count, refreshed_at) VALUES(?, ?, ?, ?)
		 ON CONFLICT(tracker, category_id) DO UPDATE SET last_count = excluded.last_count, refreshed_at = excluded.refreshed_at`,
		cat.Tracker, cat.ID, len(rs), ms(now)); err != nil {
		return err
	}
	return tx.Commit()
}

// appendEntries — порция раздела глубже загруженного: раздачи — в базу, в раздел — те, которых в нём ещё
// нет, в конец. Возвращает, сколько добавлено.
func (s catalogStore) appendEntries(ctx context.Context, cat CategoryRef, rs []source.Release, now time.Time) (int, error) {
	tx, err := s.db.W.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var next int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(position) + 1, 0) FROM catalog_entries WHERE tracker = ? AND category_id = ?`,
		cat.Tracker, cat.ID).Scan(&next); err != nil {
		return 0, err
	}
	added := 0
	for _, r := range rs {
		id, err := upsertRelease(ctx, tx, r, now)
		if err != nil {
			return 0, err
		}
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM catalog_entries WHERE tracker = ? AND category_id = ? AND release_id = ?`,
			cat.Tracker, cat.ID, id).Scan(&n); err != nil {
			return 0, err
		}
		if n > 0 {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO catalog_entries(tracker, category_id, position, release_id) VALUES(?, ?, ?, ?)`,
			cat.Tracker, cat.ID, next, id); err != nil {
			return 0, err
		}
		next++
		added++
	}
	return added, tx.Commit()
}

// sectionCount — сколько раздач в разделе сейчас (первая сотня и порции).
func (s catalogStore) sectionCount(ctx context.Context, cat CategoryRef) (int, error) {
	var n int
	err := s.db.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM catalog_entries WHERE tracker = ? AND category_id = ?`, cat.Tracker, cat.ID).Scan(&n)
	return n, err
}

// saveFound — раздачи из поиска: в базу, чтобы карточку можно было открыть (спека, раздел 7).
func (s catalogStore) saveFound(ctx context.Context, rs []source.Release, now time.Time) ([]int64, error) {
	tx, err := s.db.W.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	ids := make([]int64, len(rs))
	for i, r := range rs {
		if ids[i], err = upsertRelease(ctx, tx, r, now); err != nil {
			return nil, err
		}
	}
	return ids, tx.Commit()
}

// state — прошлое удачное обновление раздела.
func (s catalogStore) state(ctx context.Context, cat CategoryRef) (lastCount int, refreshedAt time.Time, err error) {
	var at int64
	err = s.db.R.QueryRowContext(ctx, `SELECT last_count, refreshed_at FROM catalog_state WHERE tracker = ? AND category_id = ?`,
		cat.Tracker, cat.ID).Scan(&lastCount, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, time.Time{}, nil
	}
	return lastCount, fromMS(at), err
}

// catalogRows — раздачи из топов включённых разделов, живые, по убыванию раздающих.
func (s catalogStore) catalogRows(ctx context.Context, cats []CategoryRef) ([]row, error) {
	enabled := map[CategoryRef]bool{}
	for _, c := range cats {
		enabled[c] = true
	}
	rows, err := s.db.R.QueryContext(ctx,
		`SELECT `+rowColumns+`, e.tracker, e.category_id, e.position
		 FROM catalog_entries e JOIN releases r ON r.id = e.release_id
		 WHERE r.removed = 0
		 ORDER BY r.seeders DESC, r.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []row
	seen := map[int64]bool{}
	for rows.Next() {
		var ref CategoryRef
		var pos int
		r, err := scanRow(rows, &ref.Tracker, &ref.ID, &pos)
		if err != nil {
			return nil, err
		}
		if !enabled[ref] || seen[r.ID] {
			continue
		}
		seen[r.ID] = true
		r.Section, r.Pos = ref.ID, pos
		if r.CategoryID == "" {
			r.CategoryID = ref.ID
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// rowsByID — раздачи по номерам (результаты поиска).
func (s catalogStore) rowsByID(ctx context.Context, ids []int64) (map[int64]row, error) {
	out := map[int64]row{}
	for len(ids) > 0 {
		chunk := ids[:min(len(ids), 500)]
		ids = ids[len(chunk):]
		args := make([]any, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}
		rows, err := s.db.R.QueryContext(ctx, `SELECT `+rowColumns+` FROM releases r WHERE r.id IN (?`+strings.Repeat(", ?", len(chunk)-1)+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			r, err := scanRow(rows)
			if err != nil {
				rows.Close()
				return nil, err
			}
			out[r.ID] = r
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// rowsWhere — живые раздачи по условию (часть после WHERE), по убыванию раздающих.
func (s catalogStore) rowsWhere(ctx context.Context, where string, args ...any) ([]row, error) {
	rows, err := s.db.R.QueryContext(ctx,
		`SELECT `+rowColumns+` FROM releases r WHERE r.removed = 0 AND `+where+` ORDER BY r.seeders DESC, r.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []row
	for rows.Next() {
		r, err := scanRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// rowsByKinopoisk — живые раздачи с номером Кинопоиска из описания.
func (s catalogStore) rowsByKinopoisk(ctx context.Context, ids []int) ([]row, error) {
	var out []row
	for len(ids) > 0 {
		chunk := ids[:min(len(ids), 500)]
		ids = ids[len(chunk):]
		args := make([]any, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}
		rs, err := s.rowsWhere(ctx, `r.kinopoisk_id IN (?`+strings.Repeat(", ?", len(chunk)-1)+`)`, args...)
		if err != nil {
			return nil, err
		}
		out = append(out, rs...)
	}
	return out, nil
}

// rowsByKeys — живые раздачи по «трекер:номер на трекере».
func (s catalogStore) rowsByKeys(ctx context.Context, keys []string) ([]row, error) {
	topics := map[string][]any{}
	var trackers []string
	for _, k := range keys {
		tracker, topic, ok := strings.Cut(k, ":")
		if !ok {
			continue
		}
		if _, seen := topics[tracker]; !seen {
			trackers = append(trackers, tracker)
		}
		topics[tracker] = append(topics[tracker], topic)
	}
	var out []row
	for _, tracker := range trackers {
		ts := topics[tracker]
		for len(ts) > 0 {
			chunk := ts[:min(len(ts), 500)]
			ts = ts[len(chunk):]
			rs, err := s.rowsWhere(ctx, `r.tracker = ? AND r.topic_id IN (?`+strings.Repeat(", ?", len(chunk)-1)+`)`,
				append([]any{tracker}, chunk...)...)
			if err != nil {
				return nil, err
			}
			out = append(out, rs...)
		}
	}
	return out, nil
}

// release — раздача для экрана раздачи: строка, описание, magnet, .torrent и «удалена с трекера».
func (s catalogStore) release(ctx context.Context, id int64) (r row, description, magnet string, torrent []byte, removed bool, err error) {
	r, err = scanRow(s.db.R.QueryRowContext(ctx,
		`SELECT `+rowColumns+`, r.description, r.magnet, r.torrent, r.removed FROM releases r WHERE r.id = ?`, id),
		&description, &magnet, &torrent, &removed)
	return r, description, magnet, torrent, removed, err
}

// missingPosters — раздачи без картинки (и без страницы: постер по номеру Кинопоиска — спека 11b, 5.4):
// в каталоге или тронутые после since.
// missingPosters — раздачи со страницей, но без картинки (в каталоге или тронутые с since) и адреса
// постеров их страниц ("" — на странице постера нет).
func (s catalogStore) missingPosters(ctx context.Context, since time.Time) ([]row, []string, error) {
	rows, err := s.db.R.QueryContext(ctx,
		`SELECT `+rowColumns+`, r.poster_url FROM releases r
		 WHERE r.image_key = '' AND r.removed = 0
		   AND (r.updated_at >= ? OR r.id IN (SELECT release_id FROM catalog_entries))`, ms(since))
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var out []row
	var urls []string
	for rows.Next() {
		var u string
		r, err := scanRow(rows, &u)
		if err != nil {
			return nil, nil, err
		}
		out = append(out, r)
		urls = append(urls, u)
	}
	return out, urls, rows.Err()
}

// postersWithImage — раздачи каталога и недавние с картинкой: кандидаты на постер Кинопоиска.
func (s catalogStore) postersWithImage(ctx context.Context, since time.Time) ([]row, error) {
	rows, err := s.db.R.QueryContext(ctx,
		`SELECT `+rowColumns+` FROM releases r
		 WHERE r.image_key != '' AND r.removed = 0
		   AND (r.updated_at >= ? OR r.id IN (SELECT release_id FROM catalog_entries))`, ms(since))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []row
	for rows.Next() {
		r, err := scanRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// missingTorrents — раздачи трекеров trackers (у которых есть .torrent) со страницей, но без .torrent.
func (s catalogStore) missingTorrents(ctx context.Context, trackers []string, since time.Time) ([]row, error) {
	if len(trackers) == 0 {
		return nil, nil
	}
	args := []any{ms(since)}
	for _, t := range trackers {
		args = append(args, t)
	}
	rows, err := s.db.R.QueryContext(ctx,
		`SELECT `+rowColumns+` FROM releases r
		 WHERE (r.torrent IS NULL OR length(r.torrent) = 0) AND r.details_at > 0 AND r.removed = 0
		   AND (r.updated_at >= ? OR r.id IN (SELECT release_id FROM catalog_entries))
		   AND r.tracker IN (?`+strings.Repeat(",?", len(trackers)-1)+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []row
	for rows.Next() {
		r, err := scanRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// clearImageKeys снимает картинки keys с раздач (заглушки хостингов).
func (s catalogStore) clearImageKeys(ctx context.Context, keys []string) error {
	args := make([]any, len(keys))
	for i, k := range keys {
		args[i] = k
	}
	_, err := s.db.W.ExecContext(ctx, `UPDATE releases SET image_key = '' WHERE image_key IN (?`+strings.Repeat(",?", len(keys)-1)+`)`, args...)
	return err
}

func (s catalogStore) saveImageKey(ctx context.Context, id int64, key string) error {
	_, err := s.db.W.ExecContext(ctx, `UPDATE releases SET image_key = ? WHERE id = ?`, key, id)
	return err
}

// nextToEnrich — первая в порядке каталога раздача трекера без страницы раздачи.
func (s catalogStore) nextToEnrich(ctx context.Context, tracker string, cats []CategoryRef, now time.Time) (row, bool, error) {
	rs, err := s.catalogRows(ctx, cats)
	if err != nil {
		return row{}, false, err
	}
	for _, r := range rs {
		if r.Tracker == tracker && r.DetailsAt.IsZero() && !r.RetryAt.After(now) {
			return r, true, nil
		}
	}
	return row{}, false, nil
}

// saveDetails — страница раздачи. Цифры — только ненулевые: гостю Rutracker не видны раздающие
// и размер, а свежие цифры и так приходят с топом.
func (s catalogStore) saveDetails(ctx context.Context, id int64, d source.Details, kpID int, imageKey, format string, now time.Time) error {
	_, err := s.db.W.ExecContext(ctx,
		`UPDATE releases SET
		   title = CASE WHEN ? != '' THEN ? ELSE title END,
		   description = ?, poster_url = ?, image_key = ?, kinopoisk_id = ?, imdb_id = ?, magnet = ?,
		   infohash = CASE WHEN ? != '' THEN ? ELSE infohash END,
		   seeders = CASE WHEN ? > 0 THEN ? ELSE seeders END,
		   leechers = CASE WHEN ? > 0 THEN ? ELSE leechers END,
		   size = CASE WHEN ? > 0 THEN ? ELSE size END,
		   format = ?, details_at = ?, retry_at = 0
		 WHERE id = ?`,
		d.Title, d.Title, d.Description, d.PosterURL, imageKey, kpID, d.IMDbID, d.Magnet,
		d.InfoHash, d.InfoHash, d.Seeders, d.Seeders, d.Leechers, d.Leechers, d.Size, d.Size, format, ms(now), id)
	return err
}

// formatRow — раздача, чей формат ещё не определяли: описание и .torrent, по которым он определяется.
type formatRow struct {
	id          int64
	description string
	torrent     []byte
}

// formatless — раздачи с загруженной страницей без определённого формата (загружены до миграции
// 0008), не больше limit.
func (s catalogStore) formatless(ctx context.Context, limit int) ([]formatRow, error) {
	rows, err := s.db.R.QueryContext(ctx,
		`SELECT id, description, torrent FROM releases WHERE format IS NULL AND details_at > 0 LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []formatRow
	for rows.Next() {
		var r formatRow
		if err := rows.Scan(&r.id, &r.description, &r.torrent); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s catalogStore) saveFormat(ctx context.Context, id int64, format string) error {
	_, err := s.db.W.ExecContext(ctx, `UPDATE releases SET format = ? WHERE id = ?`, format, id)
	return err
}

func (s catalogStore) saveTorrent(ctx context.Context, id int64, b []byte) error {
	_, err := s.db.W.ExecContext(ctx, `UPDATE releases SET torrent = ? WHERE id = ?`, b, id)
	return err
}

func (s catalogStore) detailsFailed(ctx context.Context, id int64, retryAt time.Time) error {
	_, err := s.db.W.ExecContext(ctx, `UPDATE releases SET retry_at = ? WHERE id = ?`, ms(retryAt), id)
	return err
}

func (s catalogStore) markRemoved(ctx context.Context, id int64) error {
	_, err := s.db.W.ExecContext(ctx, `UPDATE releases SET removed = 1 WHERE id = ?`, id)
	return err
}

// setTitleIfEmpty — название из ленты Atom, пока страница раздачи недоступна.
func (s catalogStore) setTitleIfEmpty(ctx context.Context, tracker, topicID, title string) error {
	_, err := s.db.W.ExecContext(ctx, `UPDATE releases SET title = ? WHERE tracker = ? AND topic_id = ? AND title = ''`,
		title, tracker, topicID)
	return err
}

// replaceCategories — дерево разделов трекера.
func (s catalogStore) replaceCategories(ctx context.Context, tracker string, cs []source.Category) error {
	tx, err := s.db.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM categories WHERE tracker = ?`, tracker); err != nil {
		return err
	}
	for _, c := range cs {
		if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO categories(tracker, id, name, parent_id) VALUES(?, ?, ?, ?)`,
			tracker, c.ID, c.Name, c.ParentID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// tree — дерево разделов трекера в том порядке, в каком его отдал трекер.
func (s catalogStore) tree(ctx context.Context, tracker string) ([]source.Category, error) {
	rows, err := s.db.R.QueryContext(ctx, `SELECT id, name, parent_id FROM categories WHERE tracker = ? ORDER BY rowid`, tracker)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []source.Category
	for rows.Next() {
		var c source.Category
		if err := rows.Scan(&c.ID, &c.Name, &c.ParentID); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// categoryNames — названия разделов раздач rs: по запросу на трекер, а не на каждую раздачу (ревью
// 5c: ~370 запросов на один опрос поиска).
func (s catalogStore) categoryNames(ctx context.Context, rs []row) (map[CategoryRef]string, error) {
	out := map[CategoryRef]string{}
	done := map[string]bool{}
	for _, r := range rs {
		if done[r.Tracker] {
			continue
		}
		done[r.Tracker] = true
		rows, err := s.db.R.QueryContext(ctx, `SELECT id, name FROM categories WHERE tracker = ?`, r.Tracker)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id, name string
			if err := rows.Scan(&id, &name); err != nil {
				rows.Close()
				return nil, err
			}
			out[CategoryRef{r.Tracker, id}] = name
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s catalogStore) categoryName(ctx context.Context, tracker, id string) string {
	var name string
	if err := s.db.R.QueryRowContext(ctx, `SELECT name FROM categories WHERE tracker = ? AND id = ?`, tracker, id).Scan(&name); err != nil {
		return id
	}
	return name
}

// imageKeysInUse — картинки, которые каталог показывает или скоро может показать: у раздач в
// каталоге и у тронутых после since (найденные поиском, недавно выпавшие из топа).
func (st *catalogStore) imageKeysInUse(ctx context.Context, since time.Time) (map[string]bool, error) {
	rows, err := st.db.R.QueryContext(ctx,
		`SELECT DISTINCT image_key FROM releases WHERE image_key != ''
		   AND (updated_at >= ? OR id IN (SELECT release_id FROM catalog_entries))`, since.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	keep := map[string]bool{}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		keep[k] = true
	}
	return keep, rows.Err()
}

// forgetImages — этих картинок больше нет в кэше: у раздач — без постера.
func (st *catalogStore) forgetImages(ctx context.Context, keys []string) error {
	for _, k := range keys {
		if _, err := st.db.W.ExecContext(ctx, `UPDATE releases SET image_key = '' WHERE image_key = ?`, k); err != nil {
			return err
		}
	}
	return nil
}
