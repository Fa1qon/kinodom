package library

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
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
	if pathKey(key) == pathKey(root) {
		files, err := l.unitFiles(ctx, id)
		if err != nil {
			return err
		}
		for _, p := range files {
			if !within(p, root) || pathKey(p) == pathKey(root) {
				continue
			}
			if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return removeError(root, err)
			}
		}
	} else if err := os.RemoveAll(key); err != nil {
		return removeError(root, err)
	}
	if _, err := l.d.W.ExecContext(ctx, `DELETE FROM lib_units WHERE id = ?`, id); err != nil {
		return err
	}
	l.startScan(true)
	return nil
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

func removeError(root string, err error) error {
	if errors.Is(err, fs.ErrPermission) {
		return fmt.Errorf("%w %s — «Разрешить доступ» в настройках медиатеки", ErrNoWrite, root)
	}
	return fmt.Errorf("не удалось удалить: %w", err)
}
