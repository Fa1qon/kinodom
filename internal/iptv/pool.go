package iptv

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"kinodom/internal/iptv/m3u"
	"kinodom/internal/store"
)

// Playlist — плейлист в пуле.
type Playlist struct {
	ID          int64
	Name        string
	URL         string // "" — файл
	Limited     bool   // «ограничено число просмотров»: в фоне не проверяется
	AddedAt     time.Time
	UpdatedAt   time.Time // последнее удачное обновление; ноль — не было
	TriedAt     time.Time
	Error       string
	Unsupported int
}

// Entry — запись плейлиста: как источник подписан в этом плейлисте.
type Entry struct {
	Playlist int64
	m3u.Entry
}

// Состояния источника (спека этапа 8, раздел 5.8).
const (
	StateNew    = "new"    // ещё не проверен
	StateAlive  = "alive"  // последняя проверка прошла
	StateSilent = "silent" // последняя проверка — «не отвечает»
	StateDead   = "dead"   // три «не отвечает» подряд
)

// Оценки проверки.
const (
	GradeGreen   = "green"
	GradeYellow  = "yellow"
	GradeRed     = "red"
	GradeBlack   = "black"   // не отвечает
	GradeAlive   = "alive"   // лёгкая проверка прошла
	GradeUnrated = "unrated" // жив, полной проверки не было — так его показывает API
)

// Stream — источник: одна ссылка.
type Stream struct {
	ID      int64
	URL     string
	Kind    string // hls, dash, live; "" — неизвестен
	Quality string // по проверке; "" — по названию записей
	State   string
	Fails   int
	Grade   string // последняя полная проверка: green, yellow, red; "" — не было
	LightAt time.Time
	FullAt  time.Time
	TTFB    int // мс
	Ratio   float64
	Mbps    float64
	Error   string
	Audio   *bool // есть ли звук, по полной проверке; nil — не знаем
	Entries []Entry
}

// Rule — ручная правка сопоставления: канал или «скрыт».
type Rule struct {
	Channel string
	Hidden  bool
}

// Override — ручные правки канала; nil / пусто — не правили.
type Override struct {
	Hidden     bool
	Category   *string
	Country    *string
	Languages  []string // nil — не правили
	PinnedURL  string
	HiddenURLs []string // скрытые у канала источники: не предлагаются, пока есть другие
}

func (o Override) empty() bool {
	return !o.Hidden && o.Category == nil && o.Country == nil && o.Languages == nil && o.PinnedURL == "" && len(o.HiddenURLs) == 0
}

// pool — всё, из чего собирается состав каналов: плейлисты, источники с записями, правки.
type pool struct {
	playlists   map[int64]*Playlist
	streams     map[int64]*Stream
	byURL       map[string]*Stream
	streamRules map[string]Rule // ссылка → правка
	nameRules   map[string]Rule // нормализованное название → правка
	overrides   map[string]Override
	custom      map[string]Custom // свои каналы (план 14Д)
}

func newPool() *pool {
	return &pool{playlists: map[int64]*Playlist{}, streams: map[int64]*Stream{}, byURL: map[string]*Stream{},
		streamRules: map[string]Rule{}, nameRules: map[string]Rule{}, overrides: map[string]Override{}, custom: map[string]Custom{}}
}

// db — таблицы IPTV.
type db struct{ *store.DB }

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

// load читает пул из базы одной транзакцией.
func (d db) load(ctx context.Context) (*pool, error) {
	// Одна транзакция чтения: плейлисты, источники, записи и правки — из одного состояния базы.
	tx, err := d.R.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	p := newPool()
	rows, err := tx.QueryContext(ctx, `SELECT id, name, url, limited, added_at, updated_at, tried_at, error, unsupported FROM iptv_playlists`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var pl Playlist
		var added, updated, tried int64
		if err := rows.Scan(&pl.ID, &pl.Name, &pl.URL, &pl.Limited, &added, &updated, &tried, &pl.Error, &pl.Unsupported); err != nil {
			rows.Close()
			return nil, err
		}
		pl.AddedAt, pl.UpdatedAt, pl.TriedAt = fromMS(added), fromMS(updated), fromMS(tried)
		p.playlists[pl.ID] = &pl
	}
	rows.Close()
	rows, err = tx.QueryContext(ctx, `SELECT id, url, kind, quality, state, fails, grade, light_at, full_at, ttfb_ms, ratio, mbps, error, audio FROM iptv_streams`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var s Stream
		var light, full int64
		var audio sql.NullBool
		if err := rows.Scan(&s.ID, &s.URL, &s.Kind, &s.Quality, &s.State, &s.Fails, &s.Grade, &light, &full, &s.TTFB, &s.Ratio, &s.Mbps, &s.Error, &audio); err != nil {
			rows.Close()
			return nil, err
		}
		s.LightAt, s.FullAt = fromMS(light), fromMS(full)
		if audio.Valid {
			s.Audio = &audio.Bool
		}
		p.streams[s.ID] = &s
		p.byURL[s.URL] = &s
	}
	rows.Close()
	rows, err = tx.QueryContext(ctx, `SELECT playlist_id, stream_id, name, tvg_id, tvg_name, tvg_shift, logo, grp, user_agent, referrer
		FROM iptv_entries ORDER BY playlist_id, position`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var e Entry
		var sid int64
		if err := rows.Scan(&e.Playlist, &sid, &e.Name, &e.TvgID, &e.TvgName, &e.Shift, &e.Logo, &e.Group, &e.Headers.UserAgent, &e.Headers.Referrer); err != nil {
			rows.Close()
			return nil, err
		}
		if s := p.streams[sid]; s != nil {
			e.URL = s.URL
			s.Entries = append(s.Entries, e)
		}
	}
	rows.Close()
	for _, t := range []struct {
		table string
		dst   map[string]Rule
	}{{"iptv_stream_rules", p.streamRules}, {"iptv_name_rules", p.nameRules}} {
		key := "url"
		if t.table == "iptv_name_rules" {
			key = "name"
		}
		rows, err := tx.QueryContext(ctx, `SELECT `+key+`, channel, hidden FROM `+t.table)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var k string
			var r Rule
			if err := rows.Scan(&k, &r.Channel, &r.Hidden); err != nil {
				rows.Close()
				return nil, err
			}
			t.dst[k] = r
		}
		rows.Close()
	}
	rows, err = tx.QueryContext(ctx, `SELECT channel, hidden, category, country, languages, pinned_url, hidden_urls FROM iptv_channel_overrides`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		var o Override
		var cat, country, langs sql.NullString
		var hiddenURLs string
		if err := rows.Scan(&k, &o.Hidden, &cat, &country, &langs, &o.PinnedURL, &hiddenURLs); err != nil {
			return nil, err
		}
		if hiddenURLs != "" {
			o.HiddenURLs = strings.Split(hiddenURLs, "\n")
		}
		if cat.Valid {
			o.Category = &cat.String
		}
		if country.Valid {
			o.Country = &country.String
		}
		if langs.Valid {
			o.Languages = splitList(langs.String)
		}
		p.overrides[k] = o
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	rows, err = tx.QueryContext(ctx, `SELECT key, name, logo FROM iptv_custom WHERE deleted_at IS NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var c Custom
		if err := rows.Scan(&c.Key, &c.Name, &c.Logo); err != nil {
			return nil, err
		}
		p.custom[c.Key] = c
	}
	return p, rows.Err()
}

func splitList(s string) []string {
	out := []string{}
	for _, v := range strings.Split(s, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// insertPlaylist добавляет плейлист; ID — в pl.
func (d db) insertPlaylist(ctx context.Context, pl *Playlist) error {
	res, err := d.W.ExecContext(ctx, `INSERT INTO iptv_playlists (name, url, limited, added_at) VALUES (?, ?, ?, ?)`,
		pl.Name, pl.URL, pl.Limited, ms(pl.AddedAt))
	if err != nil {
		return err
	}
	pl.ID, err = res.LastInsertId()
	return err
}

// savePlaylist записывает поля плейлиста.
func (d db) savePlaylist(ctx context.Context, pl *Playlist) error {
	_, err := d.W.ExecContext(ctx, `UPDATE iptv_playlists SET name = ?, url = ?, limited = ?, updated_at = ?, tried_at = ?, error = ?, unsupported = ? WHERE id = ?`,
		pl.Name, pl.URL, pl.Limited, ms(pl.UpdatedAt), ms(pl.TriedAt), pl.Error, pl.Unsupported, pl.ID)
	return err
}

// replaceEntries заменяет записи плейлиста. Новые ссылки становятся источниками, источники без
// записей удаляются. Возвращает id источников по ссылкам и id удалённых источников.
func (d db) replaceEntries(ctx context.Context, playlist int64, entries []m3u.Entry) (map[string]int64, []int64, error) {
	tx, err := d.W.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM iptv_entries WHERE playlist_id = ?`, playlist); err != nil {
		return nil, nil, err
	}
	ins, err := tx.PrepareContext(ctx, `INSERT INTO iptv_streams (url, kind) VALUES (?, ?) ON CONFLICT(url) DO UPDATE SET url = url RETURNING id`)
	if err != nil {
		return nil, nil, err
	}
	defer ins.Close()
	ent, err := tx.PrepareContext(ctx, `INSERT INTO iptv_entries (playlist_id, position, stream_id, name, tvg_id, tvg_name, tvg_shift, logo, grp, user_agent, referrer)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return nil, nil, err
	}
	defer ent.Close()
	ids := map[string]int64{}
	for i, e := range entries {
		id, ok := ids[e.URL]
		if !ok {
			if err := ins.QueryRowContext(ctx, e.URL, m3u.KindOf(e.URL)).Scan(&id); err != nil {
				return nil, nil, err
			}
			ids[e.URL] = id
		}
		if _, err := ent.ExecContext(ctx, playlist, i, id, e.Name, e.TvgID, e.TvgName, e.Shift, e.Logo, e.Group,
			e.Headers.UserAgent, e.Headers.Referrer); err != nil {
			return nil, nil, err
		}
	}
	removed, err := deleteOrphans(ctx, tx)
	if err != nil {
		return nil, nil, err
	}
	return ids, removed, tx.Commit()
}

func deleteOrphans(ctx context.Context, tx *sql.Tx) ([]int64, error) {
	rows, err := tx.QueryContext(ctx, `DELETE FROM iptv_streams WHERE id NOT IN (SELECT stream_id FROM iptv_entries) RETURNING id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// deletePlaylist удаляет плейлист с записями и источники, у которых записей не осталось.
func (d db) deletePlaylist(ctx context.Context, id int64) ([]int64, error) {
	tx, err := d.W.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `DELETE FROM iptv_playlists WHERE id = ?`, id)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, errNoPlaylist
	}
	removed, err := deleteOrphans(ctx, tx)
	if err != nil {
		return nil, err
	}
	return removed, tx.Commit()
}

var errNoPlaylist = errors.New("такого плейлиста нет")

// check — результат проверки для записи.
type check struct {
	At     time.Time
	Level  string // light, full
	Grade  string // alive, green, yellow, red, black
	Ratio  float64
	TTFB   int
	Mbps   float64
	Error  string
	Kind   string // уточнённый вид; "" — не менять
	Height int    // высота кадра из мастер-плейлиста; 0 — неизвестна
}

// saveCheck записывает состояние источника и результат проверки.
func (d db) saveCheck(ctx context.Context, s *Stream, c check) error {
	tx, err := d.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE iptv_streams SET kind = ?, quality = ?, state = ?, fails = ?, grade = ?, light_at = ?, full_at = ?,
		ttfb_ms = ?, ratio = ?, mbps = ?, error = ?, audio = ? WHERE id = ?`,
		s.Kind, s.Quality, s.State, s.Fails, s.Grade, ms(s.LightAt), ms(s.FullAt), s.TTFB, s.Ratio, s.Mbps, s.Error, s.Audio, s.ID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO probe_results (stream_id, at, level, grade, ratio, ttfb_ms, mbps, error) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		s.ID, ms(c.At), c.Level, c.Grade, c.Ratio, c.TTFB, c.Mbps, c.Error); err != nil {
		return err
	}
	return tx.Commit()
}

// pruneChecks удаляет результаты проверок старше before.
func (d db) pruneChecks(ctx context.Context, before time.Time) error {
	_, err := d.W.ExecContext(ctx, `DELETE FROM probe_results WHERE at < ?`, ms(before))
	return err
}

// Week — полные проверки источника за неделю: всего и хороших (🟢 и 🟡), днём и вечером.
type Week struct {
	Day         int `json:"day"`
	DayGood     int `json:"dayGood"`
	Evening     int `json:"evening"` // с 19 до 23 по местному времени
	EveningGood int `json:"eveningGood"`
}

// weeks — «днём / вечером» по источникам (спека этапа 8, раздел 5.8).
func (d db) weeks(ctx context.Context, ids []int64, since time.Time, loc *time.Location) (map[int64]Week, error) {
	out := map[int64]Week{}
	if len(ids) == 0 {
		return out, nil
	}
	args := []any{ms(since)}
	marks := make([]string, len(ids))
	for i, id := range ids {
		marks[i] = "?"
		args = append(args, id)
	}
	rows, err := d.R.QueryContext(ctx, `SELECT stream_id, at, grade FROM probe_results WHERE level = 'full' AND at >= ? AND stream_id IN (`+
		strings.Join(marks, ",")+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, at int64
		var grade string
		if err := rows.Scan(&id, &at, &grade); err != nil {
			return nil, err
		}
		w := out[id]
		good := grade == GradeGreen || grade == GradeYellow
		if evening(fromMS(at).In(loc)) {
			w.Evening++
			if good {
				w.EveningGood++
			}
		} else {
			w.Day++
			if good {
				w.DayGood++
			}
		}
		out[id] = w
	}
	return out, rows.Err()
}

// goodShare — доля 🟢 и 🟡 среди полных проверок за неделю по источникам: порядок источников.
func (d db) goodShare(ctx context.Context, since time.Time) (map[int64]float64, error) {
	rows, err := d.R.QueryContext(ctx, `SELECT stream_id, SUM(grade IN ('green', 'yellow')), COUNT(*) FROM probe_results
		WHERE level = 'full' AND at >= ? GROUP BY stream_id`, ms(since))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]float64{}
	for rows.Next() {
		var id int64
		var good, all int
		if err := rows.Scan(&id, &good, &all); err != nil {
			return nil, err
		}
		if all > 0 {
			out[id] = float64(good) / float64(all)
		}
	}
	return out, rows.Err()
}

func evening(t time.Time) bool { return t.Hour() >= 19 && t.Hour() < 23 }

// setStreamRule / setNameRule — правка сопоставления; nil — снять.
func (d db) setStreamRule(ctx context.Context, url string, r *Rule) error {
	return d.setRule(ctx, "iptv_stream_rules", "url", url, r)
}

func (d db) setNameRule(ctx context.Context, name string, r *Rule) error {
	return d.setRule(ctx, "iptv_name_rules", "name", name, r)
}

func (d db) setRule(ctx context.Context, table, key, k string, r *Rule) error {
	if r == nil {
		_, err := d.W.ExecContext(ctx, `DELETE FROM `+table+` WHERE `+key+` = ?`, k)
		return err
	}
	_, err := d.W.ExecContext(ctx, `INSERT INTO `+table+` (`+key+`, channel, hidden) VALUES (?, ?, ?)
		ON CONFLICT(`+key+`) DO UPDATE SET channel = excluded.channel, hidden = excluded.hidden`, k, r.Channel, r.Hidden)
	return err
}

// setOverride — правки канала; пустые — удалить строку.
func (d db) setOverride(ctx context.Context, channel string, o Override) error {
	if o.empty() {
		_, err := d.W.ExecContext(ctx, `DELETE FROM iptv_channel_overrides WHERE channel = ?`, channel)
		return err
	}
	var langs any
	if o.Languages != nil {
		langs = strings.Join(o.Languages, ",")
	}
	_, err := d.W.ExecContext(ctx, `INSERT INTO iptv_channel_overrides (channel, hidden, category, country, languages, pinned_url, hidden_urls)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(channel) DO UPDATE SET hidden = excluded.hidden, category = excluded.category, country = excluded.country,
		languages = excluded.languages, pinned_url = excluded.pinned_url, hidden_urls = excluded.hidden_urls`,
		channel, o.Hidden, o.Category, o.Country, langs, o.PinnedURL, strings.Join(o.HiddenURLs, "\n"))
	return err
}

// favorites — избранное устройства по порядку.
func (d db) favorites(ctx context.Context, device string) ([]string, error) {
	rows, err := d.R.QueryContext(ctx, `SELECT channel FROM iptv_favorites WHERE device = ? ORDER BY position`, device)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	seen := map[string]bool{}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		// ★ — у канала: ключ версии (до 11b-Е ★ ставилась на версию) читается ключом канала.
		if k = familyOf(k); !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	return out, rows.Err()
}

// allFavorites — каналы в избранном хоть одного устройства.
func (d db) allFavorites(ctx context.Context) (map[string]bool, error) {
	rows, err := d.R.QueryContext(ctx, `SELECT DISTINCT channel FROM iptv_favorites`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out[familyOf(k)] = true // ★ — у канала (11b-Е)
	}
	return out, rows.Err()
}

// addFavorite — канал в конец избранного устройства (если его там нет).
func (d db) addFavorite(ctx context.Context, device, key string) error {
	_, err := d.W.ExecContext(ctx, `INSERT INTO iptv_favorites (device, channel, position)
		SELECT ?, ?, COALESCE(MAX(position), -1) + 1 FROM iptv_favorites WHERE device = ?
		ON CONFLICT(device, channel) DO NOTHING`, device, key, device)
	return err
}

// removeFavorite — убрать канал из избранного устройства: и ключ канала, и ключи его версий (до 11b-Е ★
// ставилась на версию — финальное ревью 11b-Е).
func (d db) removeFavorite(ctx context.Context, device, key string) error {
	rows, err := d.R.QueryContext(ctx, `SELECT channel FROM iptv_favorites WHERE device = ?`, device)
	if err != nil {
		return err
	}
	var gone []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			rows.Close()
			return err
		}
		if familyOf(k) == familyOf(key) {
			gone = append(gone, k)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, k := range gone {
		if _, err := d.W.ExecContext(ctx, `DELETE FROM iptv_favorites WHERE device = ? AND channel = ?`, device, k); err != nil {
			return err
		}
	}
	return nil
}

// setFavorites заменяет избранное устройства; повторы убираются.
func (d db) setFavorites(ctx context.Context, device string, keys []string) error {
	tx, err := d.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM iptv_favorites WHERE device = ?`, device); err != nil {
		return err
	}
	seen := map[string]bool{}
	for i, k := range keys {
		if k = familyOf(k); k == "" || seen[k] { // ★ — у канала (11b-Е)
			continue
		}
		seen[k] = true
		if _, err := tx.ExecContext(ctx, `INSERT INTO iptv_favorites (device, channel, position) VALUES (?, ?, ?)`, device, k, i); err != nil {
			return fmt.Errorf("избранное: %w", err)
		}
	}
	return tx.Commit()
}
