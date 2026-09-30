package library

import (
	"context"
	"database/sql"
	"errors"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"kinodom/internal/meta"
)

// place — сезон, раздел и серия файла по папкам на пути внутри единицы (ближайшая папка-сезон, иначе
// SxxEyy в имени; папки не-сезоны — раздел через « / ») и по имени.
func place(dirs []string, name string) (season int, section string, episode int) {
	var sec []string
	for i := len(dirs) - 1; i >= 0; i-- {
		if dirs[i] == "" || dirs[i] == "." {
			continue
		}
		if n, ok := SeasonDir(dirs[i]); ok {
			if season == 0 {
				season = n
			}
			continue
		}
		sec = append([]string{dirs[i]}, sec...)
	}
	s, ep := Episode(name)
	if season == 0 {
		season = s
	}
	return season, strings.Join(sec, " / "), ep
}

// fileOrder — порядок показа: сезон, раздел, серия, естественный порядок имён.
func fileOrder(a, b ScannedFile) int {
	if a.Season != b.Season {
		return a.Season - b.Season
	}
	if c := natCompare(a.Section, b.Section); c != 0 {
		return c
	}
	if a.Episode != b.Episode && a.Episode > 0 && b.Episode > 0 {
		return a.Episode - b.Episode
	}
	return natCompare(path.Base(strings.ReplaceAll(a.Path, `\`, "/")), path.Base(strings.ReplaceAll(b.Path, `\`, "/")))
}

// markMissing — папка категории недоступна: её единицы остаются, но не показываются.
func (d db) markMissing(ctx context.Context, folder int64) error {
	_, err := d.W.ExecContext(ctx, `UPDATE lib_units SET missing = 1 WHERE folder = ?`, folder)
	return err
}

// syncFolder — единицы папки категории по итогам обхода: новые добавляются, пропавшие удаляются,
// номера файлов с тем же путём сохраняются (на них держится история).
func (d db) syncFolder(ctx context.Context, folder int64, c Category, units []ScannedUnit, now time.Time) error {
	tx, err := d.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	existing := map[string]int64{}
	rows, err := tx.QueryContext(ctx, `SELECT id, key FROM lib_units WHERE folder = ?`, folder)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id int64
		var k string
		if err := rows.Scan(&id, &k); err != nil {
			rows.Close()
			return err
		}
		existing[k] = id
	}
	rows.Close()
	seen := map[int64]bool{}
	for _, u := range units {
		p := ParseName(u.Name)
		id, ok := existing[u.Key]
		if !ok && len(u.Files) == 0 {
			continue // одни копирующиеся файлы — единицы ещё нет, появится, когда докопируются
		}
		if !ok {
			state := StateNew
			if !c.Kinopoisk {
				state = StatePlain
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO lib_units (source, key, folder, name, title, year, state, added_at)
				VALUES ('folder', ?, ?, ?, ?, ?, ?, ?)
				ON CONFLICT(key) DO UPDATE SET folder = excluded.folder, name = excluded.name, missing = 0`,
				u.Key, folder, u.Name, p.Title, p.Year, state, ms(now)); err != nil {
				return err
			}
			if err := tx.QueryRowContext(ctx, `SELECT id FROM lib_units WHERE key = ?`, u.Key).Scan(&id); err != nil {
				return err
			}
		} else if _, err := tx.ExecContext(ctx, `UPDATE lib_units SET name = ?, title = ?, year = ?, missing = 0,
				state = CASE WHEN ? AND state = 'plain' THEN 'new' WHEN NOT ? AND state IN ('new', 'wait', 'unrecognized') THEN 'plain' ELSE state END
			WHERE id = ?`, u.Name, p.Title, p.Year, c.Kinopoisk, c.Kinopoisk, id); err != nil {
			return err
		}
		seen[id] = true
		files := make([]fileRow, len(u.Files))
		for i, f := range u.Files {
			files[i] = fileRow{Path: f.Path, TIndex: -1, Season: f.Season, Section: f.Section, Episode: f.Episode, Size: f.Size, MTime: ms(f.ModTime)}
		}
		if err := syncFiles(ctx, tx, id, files, u.Copying); err != nil {
			return err
		}
	}
	for _, id := range existing {
		if !seen[id] {
			if _, err := tx.ExecContext(ctx, `DELETE FROM lib_units WHERE id = ?`, id); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

type fileRow struct {
	Path        string
	TIndex      int
	Season      int
	Section     string
	Episode     int
	Size, MTime int64
}

// syncFiles — файлы единицы в порядке показа; строки с тем же путём сохраняют номер. copying — файлы,
// которые ещё копируются: их строки не трогаются (не удаляются и не добавляются) — хвост Х14.
func syncFiles(ctx context.Context, tx *sql.Tx, unit int64, files []fileRow, copying []string) error {
	keep := map[string]bool{}
	for _, p := range copying {
		keep[p] = true
	}
	for i, f := range files {
		keep[f.Path] = true
		if _, err := tx.ExecContext(ctx, `INSERT INTO lib_files (unit, path, tindex, season, section, episode, position, size, mtime)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(unit, path) DO UPDATE SET tindex = excluded.tindex, season = excluded.season, section = excluded.section,
				episode = excluded.episode, position = excluded.position, size = excluded.size, mtime = excluded.mtime`,
			unit, f.Path, f.TIndex, f.Season, f.Section, f.Episode, i, f.Size, f.MTime); err != nil {
			return err
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, path FROM lib_files WHERE unit = ?`, unit)
	if err != nil {
		return err
	}
	var gone []int64
	for rows.Next() {
		var id int64
		var p string
		if err := rows.Scan(&id, &p); err != nil {
			rows.Close()
			return err
		}
		if !keep[p] {
			gone = append(gone, id)
		}
	}
	rows.Close()
	for _, id := range gone {
		if _, err := tx.ExecContext(ctx, `DELETE FROM lib_files WHERE id = ?`, id); err != nil {
			return err
		}
	}
	return nil
}

// torrentFiles — файлы раздачи с сезонами и сериями, в порядке показа. Общая папка раздачи в
// начале путей — не раздел.
func torrentFiles(tu TorrentUnit) []fileRow {
	split := func(p string) []string { return strings.Split(strings.ReplaceAll(p, `\`, "/"), "/") }
	common := ""
	for i, f := range tu.Files {
		parts := split(f.Path)
		first := ""
		if len(parts) > 1 {
			first = parts[0]
		}
		if i == 0 {
			common = first
		} else if first != common {
			common = ""
		}
	}
	var sf []ScannedFile
	idx := map[string]int{}
	for _, f := range tu.Files {
		parts := split(f.Path)
		if common != "" {
			parts = parts[1:]
		}
		s, sec, ep := place(parts[:len(parts)-1], parts[len(parts)-1])
		sf = append(sf, ScannedFile{Path: f.Path, Size: f.Size, Season: s, Section: sec, Episode: ep})
		idx[f.Path] = f.Index
	}
	slices.SortFunc(sf, fileOrder)
	out := make([]fileRow, len(sf))
	for i, f := range sf {
		out[i] = fileRow{Path: f.Path, TIndex: idx[f.Path], Season: f.Season, Section: f.Section, Episode: f.Episode, Size: f.Size}
	}
	return out
}

// syncTorrents — единицы скачанного: раздача с хранимыми файлами — единица, остальные удаляются.
func (d db) syncTorrents(ctx context.Context, tus []TorrentUnit, now time.Time) error {
	tx, err := d.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	existing := map[string]int64{}
	rows, err := tx.QueryContext(ctx, `SELECT id, key FROM lib_units WHERE source = 'torrent'`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id int64
		var k string
		if err := rows.Scan(&id, &k); err != nil {
			rows.Close()
			return err
		}
		existing[k] = id
	}
	rows.Close()
	seen := map[string]bool{}
	for _, tu := range tus {
		seen[tu.Hash] = true
		if tu.Missing { // хранимые файлы есть, раздача ещё не загружена — единица остаётся как была
			continue
		}
		p := ParseName(tu.Name)
		kp := 0
		if tu.Release != nil {
			kp = tu.Release.KP
		}
		id, ok := existing[tu.Hash]
		if !ok {
			state := StateNew
			if kp > 0 {
				state = StateFound
			}
			res, err := tx.ExecContext(ctx, `INSERT INTO lib_units (source, key, name, title, year, kp_id, state, added_at)
				VALUES ('torrent', ?, ?, ?, ?, ?, ?, ?)`, tu.Hash, tu.Name, p.Title, p.Year, kp, state, ms(now))
			if err != nil {
				return err
			}
			if id, err = res.LastInsertId(); err != nil {
				return err
			}
		} else if _, err := tx.ExecContext(ctx, `UPDATE lib_units SET name = ?, title = ?, year = ?,
				kp_id = CASE WHEN ? > 0 AND state NOT IN ('manual', 'linked') THEN ? ELSE kp_id END,
				state = CASE WHEN ? > 0 AND state NOT IN ('manual', 'linked') THEN 'found' ELSE state END
			WHERE id = ?`, tu.Name, p.Title, p.Year, kp, kp, kp, id); err != nil {
			return err
		}
		if err := syncFiles(ctx, tx, id, torrentFiles(tu), nil); err != nil {
			return err
		}
	}
	for k, id := range existing {
		if !seen[k] {
			if _, err := tx.ExecContext(ctx, `DELETE FROM lib_units WHERE id = ?`, id); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

type pending struct {
	id        int64
	source    string
	key, name string
	layout    Layout
	kinopoisk bool
}

// recognizePending — поиск на Кинопоиске для новых единиц и тех, что ждали квоту; only > 0 — только
// эта единица (правка из пульта, хвост Х10).
func (l *Library) recognizePending(ctx context.Context, tus []TorrentUnit, only int64) error {
	if l.o.KP == nil {
		return nil
	}
	l.mu.Lock()
	paused := l.now().Before(l.kpPause)
	l.mu.Unlock()
	if paused {
		return nil
	}
	rows, err := l.d.R.QueryContext(ctx, `SELECT u.id, u.source, u.key, u.name, COALESCE(c.layout, ''), COALESCE(c.kinopoisk, 1)
		FROM lib_units u LEFT JOIN lib_folders f ON f.id = u.folder LEFT JOIN lib_categories c ON c.id = f.category
		WHERE (u.state = 'new' OR (u.state = 'wait' AND u.search_at <= ?)) AND u.missing = 0 AND (? = 0 OR u.id = ?) ORDER BY u.id`,
		ms(l.now()), only, only)
	if err != nil {
		return err
	}
	var todo []pending
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.id, &p.source, &p.key, &p.name, &p.layout, &p.kinopoisk); err != nil {
			rows.Close()
			return err
		}
		todo = append(todo, p)
	}
	rows.Close()
	releases := map[string]*ReleaseData{}
	for _, tu := range tus {
		releases[tu.Hash] = tu.Release
	}
	for _, p := range todo {
		if p.source == "folder" && !p.kinopoisk {
			if _, err := l.d.W.ExecContext(ctx, `UPDATE lib_units SET state = 'plain' WHERE id = ?`, p.id); err != nil {
				return err
			}
			continue
		}
		parsed := ParseName(p.name)
		var alt []string
		if r := releases[p.key]; r != nil {
			t := meta.ParseTitle(r.Title)
			alt = append(alt, r.NameRu, r.NameOrig)
			alt = append(alt, t.Names...)
			if parsed.Year == 0 {
				parsed.Year = max(r.Year, t.Year)
			}
		}
		alt = slices.DeleteFunc(alt, func(s string) bool { return strings.TrimSpace(s) == "" })
		res, err := recognize(ctx, l.o.KP, parsed, alt, p.layout, p.source == "folder")
		if err != nil {
			return err
		}
		if res.State == StateRetry { // сбой на этой единице — она позже, остальные идут дальше
			if _, err := l.d.W.ExecContext(ctx, `UPDATE lib_units SET attempts = attempts + 1,
					state = CASE WHEN attempts + 1 >= ? THEN 'unrecognized' ELSE 'wait' END, search_at = ?
				WHERE id = ? AND state IN ('new', 'wait')`, maxAttempts, ms(l.now().Add(retryAfter)), p.id); err != nil {
				return err
			}
			continue
		}
		if _, err := l.d.W.ExecContext(ctx, `UPDATE lib_units SET kp_id = ?, state = ?, search_at = 0,
				attempts = CASE WHEN ? = 'wait' THEN attempts ELSE 0 END
			WHERE id = ? AND state IN ('new', 'wait')`, res.KP, res.State, res.State, p.id); err != nil {
			return err
		}
		if res.State == StateWait { // квота, ключ — пауза для всех
			l.mu.Lock()
			l.kpPause = l.now().Add(kpPauseAfter)
			l.mu.Unlock()
			return nil
		}
	}
	return nil
}

// cardKey — ключ карточки единицы.
func cardKey(unit int64, kp int) string {
	if kp > 0 {
		return "kp-" + strconv.Itoa(kp)
	}
	return "u-" + strconv.FormatInt(unit, 10)
}

// refreshCards — данные карточек: у скачанного — со страницы раздачи (они главнее), вид — с Кинопоиска
// (Х11); у найденного в папках — с Кинопоиска один раз; карточки без единиц забываются. only > 0 — с
// Кинопоиска только этот номер (правка единицы, Х10); none — с Кинопоиска ничего.
func (l *Library) refreshCards(ctx context.Context, tus []TorrentUnit, only int, none bool) error {
	now := l.now()
	type relCard struct {
		id int64
		kp int
		r  *ReleaseData
		tu TorrentUnit
	}
	var cards []relCard
	var kps []int
	for _, tu := range tus {
		r := tu.Release
		if r == nil {
			continue
		}
		var id int64
		var kp int
		if err := l.d.R.QueryRowContext(ctx, `SELECT id, kp_id FROM lib_units WHERE key = ?`, tu.Hash).Scan(&id, &kp); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			return err
		}
		cards = append(cards, relCard{id, kp, r, tu})
		if kp > 0 {
			kps = append(kps, kp)
		}
	}
	types := map[int]string{}
	if l.o.Ratings != nil && len(kps) > 0 {
		films, err := l.o.Ratings.Films(ctx, kps)
		if err != nil {
			return err
		}
		for id, f := range films {
			types[id] = f.Type
		}
	}
	for _, c := range cards {
		id, kp, r, tu := c.id, c.kp, c.r, c.tu
		title := r.NameRu
		if title == "" {
			title = r.NameOrig
		}
		if title == "" {
			title = ParseName(tu.Name).Title
		}
		if _, err := l.d.W.ExecContext(ctx, `INSERT INTO lib_cards (key, title, name_orig, year, type, description, image_key, source, fetched_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, 'release', ?)
			ON CONFLICT(key) DO UPDATE SET title = excluded.title, name_orig = excluded.name_orig, year = excluded.year,
				type = CASE WHEN excluded.type != '' THEN excluded.type ELSE type END,
				description = excluded.description, image_key = CASE WHEN excluded.image_key != '' THEN excluded.image_key ELSE image_key END,
				source = 'release', fetched_at = excluded.fetched_at`,
			cardKey(id, kp), title, r.NameOrig, r.Year, types[kp], r.Description, r.ImageKey, ms(now)); err != nil {
			return err
		}
	}
	if !none {
		if err := l.fetchDetails(ctx, only); err != nil {
			return err
		}
	}
	_, err := l.d.W.ExecContext(ctx, `DELETE FROM lib_cards WHERE key NOT IN (
		SELECT CASE WHEN kp_id > 0 THEN 'kp-' || kp_id ELSE 'u-' || id END FROM lib_units)`)
	return err
}

// fetchDetails — описание и постер с Кинопоиска для найденных карточек без данных, вид — и для
// карточек скачанного (Х11); only > 0 — только этот номер. Сбой — повтор с паузой (Х9).
func (l *Library) fetchDetails(ctx context.Context, only int) error {
	if l.o.KP == nil {
		return nil
	}
	rows, err := l.d.R.QueryContext(ctx, `SELECT DISTINCT kp_id FROM lib_units WHERE kp_id > 0 AND (? = 0 OR kp_id = ?)
		AND 'kp-' || kp_id NOT IN (SELECT key FROM lib_cards WHERE source != '' AND type != '') ORDER BY kp_id`, only, only)
	if err != nil {
		return err
	}
	var ids []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	for _, id := range ids {
		now := l.now()
		l.mu.Lock()
		paused := now.Before(l.kpPause)
		l.mu.Unlock()
		if paused {
			return nil
		}
		if !l.due("details", id, now) {
			continue
		}
		d, err := l.o.KP.Details(ctx, id)
		switch {
		case ctx.Err() != nil:
			return ctx.Err()
		case kpPaused(err):
			l.mu.Lock()
			l.kpPause = l.now().Add(kpPauseAfter)
			l.mu.Unlock()
			return nil
		case err != nil:
			l.log.Warn("медиатека: фильм Кинопоиска не загрузился — повтор позже", "kp", id, "err", err)
			l.failed("details", id, now)
			continue
		}
		l.succeeded("details", id)
		key := ""
		if d.PosterURL != "" && l.o.Posters != nil {
			if k, err := l.o.Posters.Fetch(ctx, d.PosterURL, meta.Direct); err == nil {
				key = k
			} else {
				l.failed("poster", id, now)
			}
		}
		title := d.NameRu
		if title == "" {
			title = d.NameOrig
		}
		if _, err := l.d.W.ExecContext(ctx, `INSERT INTO lib_cards (key, title, name_orig, year, type, genres, description, image_key, source, fetched_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'kinopoisk', ?)
			ON CONFLICT(key) DO UPDATE SET type = excluded.type, genres = excluded.genres,
				title = CASE WHEN source = 'release' THEN title ELSE excluded.title END,
				name_orig = CASE WHEN source = 'release' THEN name_orig ELSE excluded.name_orig END,
				year = CASE WHEN source = 'release' THEN year ELSE excluded.year END,
				description = CASE WHEN source = 'release' THEN description ELSE excluded.description END,
				image_key = CASE WHEN source = 'release' AND image_key != '' THEN image_key ELSE excluded.image_key END,
				source = CASE WHEN source = 'release' THEN source ELSE 'kinopoisk' END, fetched_at = excluded.fetched_at`,
			"kp-"+strconv.Itoa(id), title, d.NameOrig, d.Year, d.Type, strings.Join(d.Genres, ","), d.Description, key, ms(l.now())); err != nil {
			return err
		}
		if l.o.Ratings != nil {
			if err := l.o.Ratings.AddFilm(ctx, d.Film); err != nil {
				l.log.Warn("медиатека: рейтинг не записался", "kp", id, "err", err)
			}
		}
	}
	return l.retryPosters(ctx, only)
}

// retryPosters — карточки с Кинопоиска без постера (не скачался): постер по номеру с паузой повтора, без
// нового запроса описания (хвост Х9).
func (l *Library) retryPosters(ctx context.Context, only int) error {
	if l.o.KPPoster == nil || l.o.Posters == nil {
		return nil
	}
	rows, err := l.d.R.QueryContext(ctx, `SELECT key FROM lib_cards WHERE source = 'kinopoisk' AND image_key = '' AND key LIKE 'kp-%'`)
	if err != nil {
		return err
	}
	var ids []int
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			rows.Close()
			return err
		}
		if id, err := strconv.Atoi(strings.TrimPrefix(key, "kp-")); err == nil && (only == 0 || id == only) {
			ids = append(ids, id)
		}
	}
	rows.Close()
	for _, id := range ids {
		now := l.now()
		if !l.due("poster", id, now) {
			continue
		}
		k, err := l.o.Posters.Fetch(ctx, l.o.KPPoster(id), meta.Direct)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			l.failed("poster", id, now)
			continue
		}
		l.succeeded("poster", id)
		if _, err := l.d.W.ExecContext(ctx, `UPDATE lib_cards SET image_key = ? WHERE key = ?`, k, "kp-"+strconv.Itoa(id)); err != nil {
			return err
		}
	}
	return nil
}

// retry — повтор после сбоя: паузы 10 мин, 1 ч, 6 ч, дальше сутки (в памяти, после перезапуска — заново).
type retry struct {
	n    int
	next time.Time
}

var libRetry = []time.Duration{10 * time.Minute, time.Hour, 6 * time.Hour, 24 * time.Hour}

func (l *Library) failed(kind string, id int, now time.Time) {
	k := kind + ":" + strconv.Itoa(id)
	l.mu.Lock()
	defer l.mu.Unlock()
	r := l.retries[k]
	r.n++
	r.next = now.Add(libRetry[min(r.n, len(libRetry))-1])
	l.retries[k] = r
}

func (l *Library) due(kind string, id int, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	r, ok := l.retries[kind+":"+strconv.Itoa(id)]
	return !ok || !now.Before(r.next)
}

func (l *Library) succeeded(kind string, id int) {
	l.mu.Lock()
	delete(l.retries, kind+":"+strconv.Itoa(id))
	l.mu.Unlock()
}
