package library

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

// Медиатека и загрузки — одна система (план 14В): скачанное Kinodom качается в папки стандартных «Фильмов» и
// «Сериалов» (TargetFolder), свои файлы медиатеки удаляются вручную (DeleteUnit).

var (
	ErrTorrentUnit = errors.New("скачанное удаляется как в «Загрузках»")
	ErrOutside     = errors.New("файлы не в папке медиатеки — Kinodom их не удаляет")
	ErrNoWrite     = errors.New("нет права изменять папку")
)

// TargetFolder — папка для скачанного стандартной категории builtin («films», «series»): первая её папка, которая
// не сама сериал (targetIn); такой нет — "".
func (l *Library) TargetFolder(ctx context.Context, builtin string) (string, error) {
	cs, err := l.d.categories(ctx)
	if err != nil {
		return "", err
	}
	for _, c := range cs {
		if c.Builtin != builtin {
			continue
		}
		f, ok, err := l.targetIn(ctx, c)
		if err != nil || !ok {
			return "", err
		}
		return f.Path, nil
	}
	return "", nil
}

// targetIn — куда качается скачанное категории c: первая её папка, которая не сама сериал («папка = сериал» — не
// для чужого скачанного, ревью 14В). Ею же обход проверяет право записи (ревью 15Б, Important 2).
func (l *Library) targetIn(ctx context.Context, c Category) (Folder, bool, error) {
	for _, f := range c.Folders {
		own, err := l.folderIsUnit(ctx, f)
		if err != nil {
			return Folder{}, false, err
		}
		if !own {
			return f, true, nil
		}
	}
	return Folder{}, false, nil
}

// folderIsUnit — папка категории сама единица медиатеки (сериал «папка = сериал»).
func (l *Library) folderIsUnit(ctx context.Context, f Folder) (bool, error) {
	rows, err := l.d.R.QueryContext(ctx, `SELECT key FROM lib_units WHERE folder = ? AND missing = 0`, f.ID)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return false, err
		}
		if pathKey(k) == pathKey(f.Path) {
			return true, nil
		}
	}
	return false, rows.Err()
}

// Writable — служба может писать в папку: Options.Writable, по умолчанию — проверка прав без записи (ревью 14В:
// пробный файл менял время папки и будил диск).
func (l *Library) Writable(dir string) bool {
	if l.o.Writable != nil {
		return l.o.Writable(dir)
	}
	return writable(dir)
}

// DeleteUnit — «Удалить» свою единицу медиатеки: её файл или папку с диска. Единица, чей ключ — сама папка
// категории (сериал «папка = сериал»), — только её видеофайлы: папка категории остаётся. Скачанное —
// ErrTorrentUnit (удаляется в «Загрузках»), ключ не внутри папки категории — ErrOutside. Корзины нет: служба
// работает под своей учётной записью, её корзину не видно.
func (l *Library) DeleteUnit(ctx context.Context, id int64) error {
	var source, key string
	var folder sql.NullInt64
	err := l.d.R.QueryRowContext(ctx, `SELECT source, key, folder FROM lib_units WHERE id = ?`, id).Scan(&source, &key, &folder)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNoUnit
	}
	if err != nil {
		return err
	}
	if source == "torrent" {
		return ErrTorrentUnit
	}
	var root string
	err = l.d.R.QueryRowContext(ctx, `SELECT path FROM lib_folders WHERE id = ?`, folder.Int64).Scan(&root)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrOutside
	}
	if err != nil {
		return err
	}
	if !within(key, root) {
		return ErrOutside
	}
	remove := l.o.Remove
	if remove == nil {
		remove = os.RemoveAll
	}
	nested, err := l.holdsOthers(ctx, key)
	if err != nil {
		return err
	}
	if pathKey(key) == pathKey(root) || nested {
		// Папка единицы — сама папка категории, или в ней папка другой категории либо раздача Kinodom: только
		// файлы единицы (ревью 14В, Important 5).
		files, err := l.unitFiles(ctx, id)
		if err != nil {
			return err
		}
		var gone []string
		for _, p := range files {
			if !within(p, root) || pathKey(p) == pathKey(root) {
				continue
			}
			if err := remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return l.removeError(ctx, folder.Int64, root, err)
			}
			gone = append(gone, p)
		}
		// Субтитры удалённых серий и опустевшие папки сезонов — тоже (ревью 14В); папка единицы остаётся.
		gone = append(gone, removeSidecars(gone, remove)...)
		removeEmptyDirs(key, gone)
	} else if err := remove(key); err != nil {
		return l.removeError(ctx, folder.Int64, root, err)
	}
	if _, err := l.d.W.ExecContext(ctx, `DELETE FROM lib_units WHERE id = ?`, id); err != nil {
		return err
	}
	l.setNoWrite(ctx, folder.Int64, false)
	l.startScan(true)
	return nil
}

// holdsOthers — внутри папки dir есть папка другой категории или раздача Kinodom: её целиком не удалять.
func (l *Library) holdsOthers(ctx context.Context, dir string) (bool, error) {
	paths, err := l.d.allFolderPaths(ctx)
	if err != nil {
		return false, err
	}
	if l.o.TorrentFolders != nil {
		ts, err := l.o.TorrentFolders(ctx)
		if err != nil {
			return true, nil // не знаем — бережём: только файлы единицы
		}
		paths = append(paths, ts...)
	}
	for _, p := range paths {
		if pathKey(p) != pathKey(dir) && within(p, dir) {
			return true, nil
		}
	}
	return false, nil
}

// setNoWrite — «Удалить» упёрлось в права папки (on) или прошло: у папки проблема no_write и «Разрешить
// доступ» (ревью 14В, Important 4), снимается удачным удалением.
func (l *Library) setNoWrite(ctx context.Context, folder int64, on bool) {
	l.mu.Lock()
	if l.noWrite == nil {
		l.noWrite = map[int64]bool{}
	}
	if on == l.noWrite[folder] {
		l.mu.Unlock()
		return
	}
	if on {
		l.noWrite[folder] = true
		if l.problems == nil {
			l.problems = map[int64]string{}
		}
		if l.problems[folder] == "" {
			l.problems[folder] = "no_write"
		}
	} else {
		delete(l.noWrite, folder)
		if l.problems[folder] == "no_write" {
			delete(l.problems, folder)
		}
	}
	problems := maps.Clone(l.problems)
	l.mu.Unlock()
	if cats, err := l.d.categories(ctx); err == nil {
		if err := l.syncProblems(ctx, cats, problems); err != nil {
			l.log.Warn("медиатека: проблемы папок не записались", "err", err)
		}
	}
}

// unitFiles — пути файлов единицы из папки.
func (l *Library) unitFiles(ctx context.Context, id int64) ([]string, error) {
	rows, err := l.d.R.QueryContext(ctx, `SELECT path FROM lib_files WHERE unit = ?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (l *Library) removeError(ctx context.Context, folder int64, root string, err error) error {
	if busy(err) {
		// Файл держит и сам Kinodom, пока серию смотрят на ТВ (ревью 15Б, Minor 1).
		return errors.New("файл сейчас смотрят или он открыт в другой программе — закройте её и повторите")
	}
	if errors.Is(err, fs.ErrPermission) {
		l.setNoWrite(ctx, folder, true)
		return fmt.Errorf("%w %s — «Разрешить доступ» в настройках медиатеки", ErrNoWrite, root)
	}
	return fmt.Errorf("не удалось удалить: %w", err)
}

// subtitleExt — субтитры и их спутники: удаляются вместе с серией того же имени.
var subtitleExt = map[string]bool{".srt": true, ".ass": true, ".ssa": true, ".sub": true, ".idx": true, ".vtt": true, ".smi": true, ".sup": true}

// subsDirs — папки субтитров рядом с видео.
var subsDirs = map[string]bool{"subs": true, "subtitles": true, "sub": true, "субтитры": true}

// findSidecars — субтитры и спутники видео videos: файлы, чьё имя начинается с имени видео без расширения и точки
// («Сериал.S01E01.rus.srt»), в папке видео и в её «Subs»/«Subtitles». Не по всей папке сериала: там может лежать
// раздача Kinodom другого сериала или «Extras» с тем же именем серии (ревью 15Б, Important 1). По порядку пути.
func findSidecars(videos []string) []string {
	stems := map[string][]string{} // папка видео → основы имён его видео
	dirs := map[string]string{}    // где искать → папка видео, чьи основы там ищутся
	for _, v := range videos {
		dir := filepath.Dir(v)
		b := filepath.Base(v)
		stems[pathKey(dir)] = append(stems[pathKey(dir)], strings.ToLower(strings.TrimSuffix(b, filepath.Ext(b)))+".")
		dirs[dir] = dir
	}
	for dir := range maps.Clone(dirs) {
		es, _ := os.ReadDir(dir)
		for _, e := range es {
			if e.IsDir() && subsDirs[strings.ToLower(e.Name())] {
				dirs[filepath.Join(dir, e.Name())] = dir
			}
		}
	}
	var out []string
	for at, owner := range dirs {
		es, _ := os.ReadDir(at)
		for _, e := range es {
			if e.IsDir() || !subtitleExt[strings.ToLower(filepath.Ext(e.Name()))] {
				continue
			}
			name := strings.ToLower(e.Name())
			for _, s := range stems[pathKey(owner)] {
				if strings.HasPrefix(name, s) {
					out = append(out, filepath.Join(at, e.Name()))
					break
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

// removeSidecars — удалить субтитры удалённых видео videos (findSidecars); удалённые — в ответе.
func removeSidecars(videos []string, remove func(string) error) []string {
	var out []string
	for _, p := range findSidecars(videos) {
		if remove(p) == nil {
			out = append(out, p)
		}
	}
	return out
}

// removeEmptyDirs — опустевшие папки удалённых файлов gone до папки top (её саму — нет): все папки на пути,
// глубокие первыми — папка сезона пустеет, только когда ушла её «Subtitles» (ревью 15Б). Не пустая — остаётся.
func removeEmptyDirs(top string, gone []string) {
	dirs := map[string]string{}
	for _, p := range gone {
		for d := filepath.Dir(p); within(d, top) && pathKey(d) != pathKey(top); d = filepath.Dir(d) {
			if _, ok := dirs[pathKey(d)]; ok {
				break
			}
			dirs[pathKey(d)] = d
		}
	}
	order := slices.Collect(maps.Values(dirs))
	slices.SortFunc(order, func(a, b string) int { return len(b) - len(a) })
	for _, d := range order {
		os.Remove(d) // не пустая (или нет прав) — остаётся
	}
}

// allFolderPaths — пути всех папок категорий.
func (d db) allFolderPaths(ctx context.Context) ([]string, error) {
	rows, err := d.R.QueryContext(ctx, `SELECT path FROM lib_folders`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
