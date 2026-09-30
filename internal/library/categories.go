package library

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"kinodom/internal/torrents"
)

// Layout — как делятся папки категории на единицы (спека, раздел 5.2).
type Layout string

const (
	LayoutFilms  Layout = "films"  // «как фильмы»: файл или подпапка — фильм
	LayoutSeries Layout = "series" // «как сериалы»: подпапка — сериал, курс, подкаст
)

// Category — категория медиатеки.
type Category struct {
	ID        int64    `json:"id"`
	Name      string   `json:"name"`
	Layout    Layout   `json:"layout"`
	Kinopoisk bool     `json:"kinopoisk"`
	Hidden    bool     `json:"hidden"`
	Builtin   string   `json:"builtin"` // films, series — стандартные; "" — своя
	Position  int      `json:"position"`
	Folders   []Folder `json:"folders"`
	OnDevice  bool     `json:"onDevice"` // скрытая показывается на устройстве запроса
}

// Folder — папка категории.
type Folder struct {
	ID       int64  `json:"id"`
	Category int64  `json:"category"`
	Path     string `json:"path"`
	Problem  string `json:"problem"` // "", not_found, no_access — заполняет обход
}

// CategoryInput — категория из пульта. У стандартной берутся только папки.
type CategoryInput struct {
	Name      string   `json:"name"`
	Layout    Layout   `json:"layout"`
	Kinopoisk bool     `json:"kinopoisk"`
	Hidden    bool     `json:"hidden"`
	Folders   []string `json:"folders"`
}

var (
	ErrBuiltin           = errors.New("стандартную категорию нельзя удалить или переименовать")
	ErrNoCategory        = errors.New("такой категории нет")
	ErrNoName            = errors.New("у категории должно быть название")
	ErrBadLayout         = errors.New("устройство категории — «как фильмы» или «как сериалы»")
	ErrFolderTaken       = errors.New("эта папка уже в медиатеке")
	ErrFolderInDownloads = errors.New("папка загрузок Kinodom и папки внутри неё не добавляются — скачанное и так в медиатеке")
	ErrNotLocal          = errors.New(`папки медиатеки — только на дисках этого компьютера, полным путём (D:\Share\Movies)`)
	ErrFolderMissing     = errors.New("папки нет")
	ErrFolderAccess      = errors.New("папка не читается — нет прав")
)

// pathKey — путь для сравнения: без регистра и «\» на конце.
func pathKey(p string) string {
	return strings.ToLower(strings.TrimRight(filepath.Clean(p), `\/`))
}

// within — p совпадает с dir или лежит внутри неё.
func within(p, dir string) bool {
	k, d := pathKey(p), pathKey(dir)
	return k == d || strings.HasPrefix(k, d+`\`)
}

// checkFolder — папку можно добавить в категорию: локальный диск, есть, читается, не папка загрузок.
func checkFolder(path, downloads string) error {
	if !filepath.IsAbs(path) || filepath.VolumeName(path) == "" || strings.HasPrefix(path, `\\`) || torrents.IsNetworkPath(path) {
		return fmt.Errorf("%w: %s", ErrNotLocal, path)
	}
	if downloads != "" && within(path, downloads) {
		return fmt.Errorf("%w: %s", ErrFolderInDownloads, path)
	}
	return readable(path)
}

// readable — папка есть и её содержимое читается.
func readable(path string) error {
	fi, err := os.Stat(path)
	switch {
	case errors.Is(err, os.ErrPermission):
		return fmt.Errorf("%w: %s", ErrFolderAccess, path)
	case err != nil || !fi.IsDir():
		return fmt.Errorf("%w: %s", ErrFolderMissing, path)
	}
	f, err := os.Open(path)
	if err == nil {
		_, err = f.Readdirnames(1)
		f.Close()
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: %s", ErrFolderAccess, path)
	}
	return nil
}

func (d db) categories(ctx context.Context) ([]Category, error) {
	rows, err := d.R.QueryContext(ctx, `SELECT id, name, layout, kinopoisk, hidden, builtin, position FROM lib_categories ORDER BY position, id`)
	if err != nil {
		return nil, err
	}
	var out []Category
	byID := map[int64]int{}
	for rows.Next() {
		var c Category
		if err := rows.Scan(&c.ID, &c.Name, &c.Layout, &c.Kinopoisk, &c.Hidden, &c.Builtin, &c.Position); err != nil {
			rows.Close()
			return nil, err
		}
		c.Folders = []Folder{}
		byID[c.ID] = len(out)
		out = append(out, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = d.R.QueryContext(ctx, `SELECT id, category, path FROM lib_folders ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var f Folder
		if err := rows.Scan(&f.ID, &f.Category, &f.Path); err != nil {
			return nil, err
		}
		if i, ok := byID[f.Category]; ok {
			out[i].Folders = append(out[i].Folders, f)
		}
	}
	return out, rows.Err()
}

func checkInput(in CategoryInput) error {
	if strings.TrimSpace(in.Name) == "" {
		return ErrNoName
	}
	if in.Layout != LayoutFilms && in.Layout != LayoutSeries {
		return ErrBadLayout
	}
	return nil
}

func (d db) addCategory(ctx context.Context, in CategoryInput) (int64, error) {
	if err := checkInput(in); err != nil {
		return 0, err
	}
	tx, err := d.W.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `INSERT INTO lib_categories (name, layout, kinopoisk, hidden, position)
		VALUES (?, ?, ?, ?, (SELECT COALESCE(MAX(position), 0) + 1 FROM lib_categories))`,
		strings.TrimSpace(in.Name), in.Layout, in.Kinopoisk, in.Hidden)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if err := setFolders(ctx, tx, id, in.Folders); err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

func (d db) updateCategory(ctx context.Context, id int64, in CategoryInput) error {
	tx, err := d.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var builtin string
	switch err := tx.QueryRowContext(ctx, `SELECT builtin FROM lib_categories WHERE id = ?`, id).Scan(&builtin); {
	case errors.Is(err, sql.ErrNoRows):
		return ErrNoCategory
	case err != nil:
		return err
	}
	if builtin == "" {
		if err := checkInput(in); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE lib_categories SET name = ?, layout = ?, kinopoisk = ?, hidden = ? WHERE id = ?`,
			strings.TrimSpace(in.Name), in.Layout, in.Kinopoisk, in.Hidden, id); err != nil {
			return err
		}
	}
	if err := setFolders(ctx, tx, id, in.Folders); err != nil {
		return err
	}
	return tx.Commit()
}

// setFolders — папки категории целиком: лишние уходят (с их единицами), новые добавляются.
func setFolders(ctx context.Context, tx *sql.Tx, category int64, paths []string) error {
	want := map[string]string{}
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if len(p) > 3 { // «D:\» оставить как есть
			p = strings.TrimRight(p, `\/`)
		}
		want[pathKey(p)] = p
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, path_key FROM lib_folders WHERE category = ?`, category)
	if err != nil {
		return err
	}
	have := map[string]int64{}
	for rows.Next() {
		var id int64
		var k string
		if err := rows.Scan(&id, &k); err != nil {
			rows.Close()
			return err
		}
		have[k] = id
	}
	rows.Close()
	for k, id := range have {
		if _, ok := want[k]; !ok {
			if _, err := tx.ExecContext(ctx, `DELETE FROM lib_folders WHERE id = ?`, id); err != nil {
				return err
			}
		}
	}
	for k, p := range want {
		if _, ok := have[k]; ok {
			continue
		}
		var other int64
		if err := tx.QueryRowContext(ctx, `SELECT category FROM lib_folders WHERE path_key = ?`, k).Scan(&other); err == nil {
			return fmt.Errorf("%w: %s", ErrFolderTaken, p)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO lib_folders (category, path, path_key) VALUES (?, ?, ?)`, category, p, k); err != nil {
			return err
		}
	}
	return nil
}

func (d db) deleteCategory(ctx context.Context, id int64) error {
	tx, err := d.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var builtin string
	switch err := tx.QueryRowContext(ctx, `SELECT builtin FROM lib_categories WHERE id = ?`, id).Scan(&builtin); {
	case errors.Is(err, sql.ErrNoRows):
		return ErrNoCategory
	case err != nil:
		return err
	case builtin != "":
		return ErrBuiltin
	}
	if _, err := tx.ExecContext(ctx, `UPDATE lib_cards SET category = NULL WHERE category = ?`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM lib_categories WHERE id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// deviceCategories — скрытые категории, включённые на устройстве.
func (d db) deviceCategories(ctx context.Context, device string) (map[int64]bool, error) {
	rows, err := d.R.QueryContext(ctx, `SELECT category FROM lib_devices WHERE device = ?`, device)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

func (d db) setDeviceCategory(ctx context.Context, device string, id int64, on bool) error {
	if !on {
		_, err := d.W.ExecContext(ctx, `DELETE FROM lib_devices WHERE device = ? AND category = ?`, device, id)
		return err
	}
	var n int
	if err := d.W.QueryRowContext(ctx, `SELECT COUNT(*) FROM lib_categories WHERE id = ?`, id).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return ErrNoCategory
	}
	_, err := d.W.ExecContext(ctx, `INSERT OR IGNORE INTO lib_devices (device, category) VALUES (?, ?)`, device, id)
	return err
}
