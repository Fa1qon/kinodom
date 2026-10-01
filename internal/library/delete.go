package library

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
)

// Медиатека и загрузки — одна система (план 14В): скачанное Kinodom качается в папки стандартных «Фильмов» и
// «Сериалов» (TargetFolder), свои файлы медиатеки удаляются вручную (DeleteUnit).

var (
	ErrTorrentUnit = errors.New("скачанное удаляется как в «Загрузках»")
	ErrOutside     = errors.New("путь единицы — не внутри папки её категории: удалять нечего")
	ErrNoWrite     = errors.New("нет права изменять папку")
)

// TargetFolder — папка для скачанного стандартной категории builtin («films», «series»): первая её папка;
// папок нет — "".
func (l *Library) TargetFolder(ctx context.Context, builtin string) (string, error) {
	cs, err := l.d.categories(ctx)
	if err != nil {
		return "", err
	}
	for _, c := range cs {
		if c.Builtin == builtin && len(c.Folders) > 0 {
			return c.Folders[0].Path, nil
		}
	}
	return "", nil
}

// Writable — служба может писать в папку: Options.Writable, по умолчанию — проба файлом.
func (l *Library) Writable(dir string) bool {
	if l.o.Writable != nil {
		return l.o.Writable(dir)
	}
	f, err := os.CreateTemp(dir, ".kinodom-*.tmp")
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return true
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
		for _, p := range files {
			if !within(p, root) || pathKey(p) == pathKey(root) {
				continue
			}
			if err := remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return l.removeError(ctx, folder.Int64, root, err)
			}
		}
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
	if errors.Is(err, fs.ErrPermission) {
		l.setNoWrite(ctx, folder, true)
		return fmt.Errorf("%w %s — «Разрешить доступ» в настройках медиатеки", ErrNoWrite, root)
	}
	return fmt.Errorf("не удалось удалить: %w", err)
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
