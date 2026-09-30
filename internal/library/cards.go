package library

import (
	"cmp"
	"context"
	"database/sql"

	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"kinodom/internal/history"
	"kinodom/internal/meta"
)

// CardSummary — карточка в списке медиатеки.
type CardSummary struct {
	Key          string  `json:"key"`
	Title        string  `json:"title"`
	Poster       string  `json:"poster"` // адрес картинки; "" — нет
	Year         int     `json:"year"`
	Rating       float64 `json:"rating"` // Кинопоиск; 0 — нет
	Dupes        bool    `json:"dupes"`
	Downloading  bool    `json:"downloading"`
	DeleteInDays *int    `json:"deleteInDays"` // null — не удалится по сроку (есть папка или не открывали)
	Category     int64   `json:"category"`
}

// Continue — что продолжать на устройстве.
type Continue struct {
	Card        CardSummary `json:"card"`
	Hash        string      `json:"hash"`
	File        int64       `json:"file"` // номер файла медиатеки: «Смотреть» — /library/files/{file}/play
	Name        string      `json:"name"`
	Season      int         `json:"season"`
	Episode     int         `json:"episode"`
	PositionSec float64     `json:"positionSec"` // 0 — с начала
}

// CategoryCount — вкладка медиатеки.
type CategoryCount struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Layout Layout `json:"layout"`
	Count  int    `json:"count"`
}

// ListView — экран «Медиатека» для устройства.
type ListView struct {
	Categories   []CategoryCount `json:"categories"`
	Continue     []Continue      `json:"continue"`
	Cards        []CardSummary   `json:"cards"`
	Unrecognized int             `json:"unrecognized"`
	Scan         ScanState       `json:"scan"`
}

// EpisodeView — файл версии.
type EpisodeView struct {
	File      int64                 `json:"file"`
	Hash      string                `json:"hash"`
	Index     int                   `json:"index"` // номер файла в истории: индекс файла раздачи или номер файла медиатеки
	Name      string                `json:"name"`
	Season    int                   `json:"season"`
	Section   string                `json:"section"`
	Episode   int                   `json:"episode"`
	Size      int64                 `json:"size"`
	Readiness string                `json:"readiness"` // у скачанного — цвет готовности; у папки — done
	Progress  *history.FileProgress `json:"progress"`
}

// VersionView — версия карточки: скачанная раздача или папка.
type VersionView struct {
	Unit     int64         `json:"unit"`
	Source   string        `json:"source"` // «Скачано» или название категории
	Quality  string        `json:"quality"`
	Format   string        `json:"format"`
	Path     string        `json:"path"`
	Size     int64         `json:"size"`
	Hash     string        `json:"hash"`
	Episodes []EpisodeView `json:"episodes"`
}

// CardView — карточка целиком.
type CardView struct {
	CardSummary
	NameOrig    string        `json:"nameOrig"`
	Description string        `json:"description"`
	Type        string        `json:"type"`
	Genres      []string      `json:"genres"`
	RatingIMDb  float64       `json:"ratingImdb"`
	Versions    []VersionView `json:"versions"`
	LastVersion int64         `json:"lastVersion"` // единица, которую устройство смотрело последней; 0 — нет
}

type unitRow struct {
	ID          int64
	Source      string
	Key         string
	Name        string
	Title       string
	Year        int
	KP          int
	State       string
	ManualTitle string
	ManualYear  int
	Category    int64 // категория папки; 0 у скачанного
	AddedAt     time.Time
	tu          *TorrentUnit
}

// hash — «раздача» единицы в истории.
func (u *unitRow) hash() string {
	if u.Source == "torrent" {
		return u.Key
	}
	return "lib-" + strconv.FormatInt(u.ID, 10)
}

type cardRow struct {
	Title, NameOrig, Type, Genres, Description, ImageKey, Source string
	Year                                                         int
	Category                                                     sql.NullInt64
}

// view — всё, что нужно экранам медиатеки на устройстве, одним чтением.
type view struct {
	cats    []Category
	byID    map[int64]Category
	builtin map[string]int64
	visible map[int64]bool
	units   map[string][]*unitRow // ключ карточки → версии
	byHash  map[string]*unitRow
	rows    map[string]cardRow
	ratings map[int]meta.Rating
}

func (l *Library) view(ctx context.Context, device string) (*view, error) {
	cats, err := l.d.categories(ctx)
	if err != nil {
		return nil, err
	}
	on, err := l.d.deviceCategories(ctx, device)
	if err != nil {
		return nil, err
	}
	v := &view{cats: cats, byID: map[int64]Category{}, builtin: map[string]int64{}, visible: map[int64]bool{},
		units: map[string][]*unitRow{}, byHash: map[string]*unitRow{}, rows: map[string]cardRow{}}
	for _, c := range cats {
		v.byID[c.ID] = c
		if c.Builtin != "" {
			v.builtin[c.Builtin] = c.ID
		}
		v.visible[c.ID] = !c.Hidden || on[c.ID]
	}
	tus, err := l.torrents(ctx)
	if err != nil {
		return nil, err
	}
	live := map[string]*TorrentUnit{}
	for i := range tus {
		live[tus[i].Hash] = &tus[i]
	}
	rows, err := l.d.R.QueryContext(ctx, `SELECT u.id, u.source, u.key, u.name, u.title, u.year, u.kp_id, u.state, u.manual_title, u.manual_year,
		COALESCE(f.category, 0), u.added_at FROM lib_units u LEFT JOIN lib_folders f ON f.id = u.folder
		WHERE u.missing = 0 ORDER BY u.added_at, u.id`)
	if err != nil {
		return nil, err
	}
	var kps []int
	for rows.Next() {
		u := &unitRow{}
		var added int64
		if err := rows.Scan(&u.ID, &u.Source, &u.Key, &u.Name, &u.Title, &u.Year, &u.KP, &u.State, &u.ManualTitle, &u.ManualYear,
			&u.Category, &added); err != nil {
			rows.Close()
			return nil, err
		}
		u.AddedAt = fromMS(added)
		if u.Source == "torrent" {
			if u.tu = live[u.Key]; u.tu == nil {
				continue // раздачу удалили — единица уйдёт при следующем обходе
			}
		}
		k := cardKey(u.ID, u.KP)
		v.units[k] = append(v.units[k], u)
		v.byHash[u.hash()] = u
		if u.KP > 0 {
			kps = append(kps, u.KP)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = l.d.R.QueryContext(ctx, `SELECT key, title, name_orig, year, type, genres, description, image_key, source, category FROM lib_cards`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var k string
		var r cardRow
		if err := rows.Scan(&k, &r.Title, &r.NameOrig, &r.Year, &r.Type, &r.Genres, &r.Description, &r.ImageKey, &r.Source, &r.Category); err != nil {
			rows.Close()
			return nil, err
		}
		v.rows[k] = r
	}
	rows.Close()
	v.ratings = map[int]meta.Rating{}
	if l.o.Ratings != nil && len(kps) > 0 {
		if v.ratings, err = l.o.Ratings.Films(ctx, kps); err != nil {
			return nil, err
		}
	}
	return v, nil
}

// category — категория карточки: ручной перенос; иначе папка первой версии; иначе по типу
// Кинопоиска; без типа — по числу файлов скачанного.
func (v *view) category(key string) int64 {
	r := v.rows[key]
	if r.Category.Valid {
		if _, ok := v.byID[r.Category.Int64]; ok {
			return r.Category.Int64
		}
	}
	us := v.units[key]
	for _, u := range us {
		if u.Category > 0 {
			return u.Category
		}
	}
	switch {
	case seriesTypes[r.Type]:
		return v.builtin["series"]
	case filmTypes[r.Type]:
		return v.builtin["films"]
	}
	for _, u := range us {
		if u.tu != nil && len(u.tu.Files) > 1 {
			return v.builtin["series"]
		}
	}
	return v.builtin["films"]
}

// shown — карточка есть и видна на устройстве.
func (v *view) shown(key string) bool {
	return len(v.units[key]) > 0 && v.visible[v.category(key)]
}

func (l *Library) summary(v *view, key string) CardSummary {
	us := v.units[key]
	r := v.rows[key]
	s := CardSummary{Key: key, Title: r.Title, Year: r.Year, Dupes: len(us) > 1, Category: v.category(key)}
	first := us[0]
	if s.Title == "" {
		s.Title, s.Year = first.Title, first.Year
		if first.ManualTitle != "" {
			s.Title, s.Year = first.ManualTitle, first.ManualYear
		}
		if s.Title == "" {
			s.Title = first.Name
		}
	}
	if r.ImageKey != "" {
		s.Poster = "/img/" + r.ImageKey
	} else {
		for _, u := range us {
			if u.Source == "folder" && localPoster(u.Key) != "" {
				s.Poster = "/api/v1/library/units/" + strconv.FormatInt(u.ID, 10) + "/poster"
				break
			}
		}
	}
	if first.KP > 0 {
		s.Rating = v.ratings[first.KP].Kinopoisk
	}
	var opened time.Time
	onlyTorrents := true
	for _, u := range us {
		if u.tu == nil {
			onlyTorrents = false
			continue
		}
		for _, f := range u.tu.Files {
			if f.Stored && f.Done < f.Size {
				s.Downloading = true
			}
		}
		if u.tu.LastOpened.IsZero() {
			onlyTorrents = false
		} else if u.tu.LastOpened.After(opened) {
			opened = u.tu.LastOpened
		}
	}
	if onlyTorrents && !opened.IsZero() {
		left := time.Duration(l.o.KeepDays())*24*time.Hour - l.now().Sub(opened)
		days := max(int((left+24*time.Hour-1)/(24*time.Hour)), 0)
		s.DeleteInDays = &days
	}
	return s
}

// added — когда появилась первая версия карточки.
func (v *view) added(key string) time.Time {
	var t time.Time
	for _, u := range v.units[key] {
		if t.IsZero() || u.AddedAt.Before(t) {
			t = u.AddedAt
		}
	}
	return t
}

// List — экран «Медиатека»: вкладки, «Продолжить просмотр», карточки категории (0 — все), новые сверху.
func (l *Library) List(ctx context.Context, device string, category int64) (ListView, error) {
	v, err := l.view(ctx, device)
	if err != nil {
		return ListView{}, err
	}
	out := ListView{Categories: []CategoryCount{}, Cards: []CardSummary{}, Scan: l.scanState()}
	counts := map[int64]int{}
	var keys []string
	for k := range v.units {
		if !v.shown(k) {
			continue
		}
		c := v.category(k)
		counts[c]++
		if category == 0 || c == category {
			keys = append(keys, k)
		}
		for _, u := range v.units[k] {
			if u.State == StateUnrecognized {
				out.Unrecognized++
			}
		}
	}
	for _, c := range v.cats {
		if v.visible[c.ID] {
			out.Categories = append(out.Categories, CategoryCount{ID: c.ID, Name: c.Name, Layout: c.Layout, Count: counts[c.ID]})
		}
	}
	slices.SortFunc(keys, func(a, b string) int { return cmp.Or(v.added(b).Compare(v.added(a)), cmp.Compare(a, b)) })
	for _, k := range keys {
		out.Cards = append(out.Cards, l.summary(v, k))
	}
	if out.Continue, err = l.continueList(ctx, v, device); err != nil {
		return ListView{}, err
	}
	return out, nil
}

type libFile struct {
	ID      int64
	Path    string
	TIndex  int
	Season  int
	Section string
	Episode int
	Size    int64
}

// index — номер файла в истории.
func (f libFile) index() int {
	if f.TIndex >= 0 {
		return f.TIndex
	}
	return int(f.ID)
}

func (d db) files(ctx context.Context, unit int64) ([]libFile, error) {
	rows, err := d.R.QueryContext(ctx, `SELECT id, path, tindex, season, section, episode, size FROM lib_files WHERE unit = ? ORDER BY position, id`, unit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []libFile
	for rows.Next() {
		var f libFile
		if err := rows.Scan(&f.ID, &f.Path, &f.TIndex, &f.Season, &f.Section, &f.Episode, &f.Size); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// resume — файл, который продолжать (спека этапа 8, раздел 7.5; как resumeIndex в пульте): начатый и
// недосмотренный, смотренный последним; иначе следующий после последнего просмотренного.
func resume(files []libFile, progress []history.FileProgress) (libFile, *history.FileProgress, bool) {
	byIndex := map[int]*history.FileProgress{}
	pos := map[int]int{}
	for i, f := range files {
		pos[f.index()] = i
	}
	recent := slices.Clone(progress)
	slices.SortStableFunc(recent, func(a, b history.FileProgress) int { return b.UpdatedAt.Compare(a.UpdatedAt) })
	for i := range recent {
		byIndex[recent[i].Index] = &recent[i]
	}
	for i := range recent {
		p := &recent[i]
		if j, ok := pos[p.Index]; ok && !p.Watched && p.Fraction > 0 {
			return files[j], p, true
		}
	}
	for i := range recent {
		p := &recent[i]
		j, ok := pos[p.Index]
		if !ok || !p.Watched {
			continue
		}
		for k := j + 1; k < len(files); k++ {
			if q := byIndex[files[k].index()]; q == nil || !q.Watched {
				return files[k], q, true
			}
		}
		return libFile{}, nil, false
	}
	return libFile{}, nil, false
}

// maxContinue — сколько карточек в «Продолжить просмотр».
const maxContinue = 20

func (l *Library) continueList(ctx context.Context, v *view, device string) ([]Continue, error) {
	out := []Continue{}
	if l.o.History == nil {
		return out, nil
	}
	items, err := l.o.History.List(ctx, device)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, it := range items {
		u := v.byHash[it.Hash]
		if u == nil {
			continue
		}
		key := cardKey(u.ID, u.KP)
		if seen[key] || !v.shown(key) {
			continue
		}
		files, err := l.d.files(ctx, u.ID)
		if err != nil {
			return nil, err
		}
		progress, err := l.o.History.Files(ctx, device, it.Hash)
		if err != nil {
			return nil, err
		}
		f, p, ok := resume(files, progress)
		if !ok {
			continue
		}
		seen[key] = true
		c := Continue{Card: l.summary(v, key), Hash: it.Hash, File: f.ID, Name: baseName(f.Path), Season: f.Season, Episode: f.Episode}
		if p != nil && !p.Watched {
			c.PositionSec = p.PositionSec
		}
		out = append(out, c)
		if len(out) == maxContinue {
			break
		}
	}
	return out, nil
}

func baseName(p string) string { return path.Base(strings.ReplaceAll(p, `\`, "/")) }

// Card — карточка целиком для устройства; ErrNoCard — нет или скрыта.
func (l *Library) Card(ctx context.Context, device, key string) (CardView, error) {
	v, err := l.view(ctx, device)
	if err != nil {
		return CardView{}, err
	}
	if !v.shown(key) {
		return CardView{}, ErrNoCard
	}
	r := v.rows[key]
	out := CardView{CardSummary: l.summary(v, key), NameOrig: r.NameOrig, Description: r.Description, Type: r.Type,
		Genres: []string{}, Versions: []VersionView{}}
	if r.Genres != "" {
		out.Genres = strings.Split(r.Genres, ",")
	}
	if u := v.units[key][0]; u.KP > 0 {
		out.RatingIMDb = v.ratings[u.KP].IMDb
	}
	var last time.Time
	for _, u := range v.units[key] {
		files, err := l.d.files(ctx, u.ID)
		if err != nil {
			return CardView{}, err
		}
		var progress []history.FileProgress
		if l.o.History != nil {
			if progress, err = l.o.History.Files(ctx, device, u.hash()); err != nil {
				return CardView{}, err
			}
		}
		byIndex := map[int]*history.FileProgress{}
		for i := range progress {
			byIndex[progress[i].Index] = &progress[i]
			if progress[i].UpdatedAt.After(last) {
				last, out.LastVersion = progress[i].UpdatedAt, u.ID
			}
		}
		ver := VersionView{Unit: u.ID, Hash: u.hash(), Path: u.Key, Episodes: []EpisodeView{}}
		ready := map[int]string{}
		if u.tu != nil {
			ver.Source, ver.Path = "Скачано", u.tu.Dir
			if u.tu.Release != nil {
				ver.Quality = u.tu.Release.Quality
			}
			for _, f := range u.tu.Files {
				ready[f.Index] = f.Readiness
			}
		} else {
			ver.Source = v.byID[u.Category].Name
			ver.Quality = meta.ParseTitle(u.Name).Quality
		}
		if ver.Quality == "" {
			ver.Quality = meta.ParseTitle(strings.NewReplacer(".", " ", "_", " ").Replace(u.Name)).Quality
		}
		for _, f := range files {
			ver.Size += f.Size
			ep := EpisodeView{File: f.ID, Hash: ver.Hash, Index: f.index(), Name: baseName(f.Path), Season: f.Season, Section: f.Section,
				Episode: f.Episode, Size: f.Size, Readiness: "done", Progress: byIndex[f.index()]}
			if u.tu != nil {
				ep.Readiness = ready[f.TIndex]
			}
			ver.Episodes = append(ver.Episodes, ep)
		}
		if len(files) > 0 {
			ver.Format = strings.ToUpper(strings.TrimPrefix(filepath.Ext(files[0].Path), "."))
		}
		out.Versions = append(out.Versions, ver)
	}
	return out, nil
}

// HistoryInfo — карточки «раздач» истории устройства (экран «История»): скрытые и удалённые не
// попадают в ответ.
func (l *Library) HistoryInfo(ctx context.Context, device string, hashes []string) (map[string]CardSummary, error) {
	v, err := l.view(ctx, device)
	if err != nil {
		return nil, err
	}
	out := map[string]CardSummary{}
	for _, h := range hashes {
		u := v.byHash[h]
		if u == nil {
			continue
		}
		if k := cardKey(u.ID, u.KP); v.shown(k) {
			out[h] = l.summary(v, k)
		}
	}
	return out, nil
}

// SetCardCategory — перенести карточку в категорию; nil — вернуть категорию по умолчанию.
func (l *Library) SetCardCategory(ctx context.Context, key string, category *int64) error {
	if category != nil {
		var n int
		if err := l.d.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM lib_categories WHERE id = ?`, *category).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return ErrNoCategory
		}
	}
	var n int
	if err := l.d.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM lib_units WHERE CASE WHEN kp_id > 0 THEN 'kp-' || kp_id ELSE 'u-' || id END = ?`,
		key).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return ErrNoCard
	}
	_, err := l.d.W.ExecContext(ctx, `INSERT INTO lib_cards (key, category) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET category = excluded.category`, key, category)
	return err
}

// ImageKeys — постеры карточек: чистка кэша картинок их не трогает.
func (l *Library) ImageKeys(ctx context.Context) (map[string]bool, error) {
	rows, err := l.d.R.QueryContext(ctx, `SELECT image_key FROM lib_cards WHERE image_key != ''`)
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
		out[k] = true
	}
	return out, rows.Err()
}

// posterNames — постер ручной и нераспознанной единицы в её папке (спека, раздел 5.4).
var posterNames = []string{"poster.jpg", "folder.jpg", "cover.jpg", "poster.png", "folder.png", "cover.png"}

// localPoster — путь постера в папке единицы; у единицы-файла и без постера — "".
func localPoster(unitKey string) string {
	fi, err := os.Stat(unitKey)
	if err != nil || !fi.IsDir() {
		return ""
	}
	for _, n := range posterNames {
		p := filepath.Join(unitKey, n)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p
		}
	}
	return ""
}

// LocalPoster — путь постера единицы из папки; "" — нет.
func (l *Library) LocalPoster(ctx context.Context, unit int64) string {
	var key, source string
	if err := l.d.R.QueryRowContext(ctx, `SELECT key, source FROM lib_units WHERE id = ?`, unit).Scan(&key, &source); err != nil || source != "folder" {
		return ""
	}
	return localPoster(key)
}
