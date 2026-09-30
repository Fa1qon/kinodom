package library

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"kinodom/internal/store"
)

func openDB(t *testing.T) db {
	t.Helper()
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return db{s}
}

func mkdir(t *testing.T, parts ...string) string {
	t.Helper()
	p := filepath.Join(parts...)
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// После миграции — две стандартные категории; их нельзя удалить и переименовать, папки менять можно.
func TestBuiltinCategories(t *testing.T) {
	ctx := context.Background()
	d := openDB(t)
	cs, err := d.categories(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 2 || cs[0].Name != "Фильмы" || cs[0].Layout != LayoutFilms || !cs[0].Kinopoisk || cs[0].Builtin != "films" ||
		cs[1].Name != "Сериалы" || cs[1].Layout != LayoutSeries || !cs[1].Kinopoisk || cs[1].Builtin != "series" {
		t.Fatalf("стандартные: %+v", cs)
	}
	if err := d.deleteCategory(ctx, cs[0].ID); !errors.Is(err, ErrBuiltin) {
		t.Errorf("удаление «Фильмов»: %v", err)
	}
	movies := mkdir(t, t.TempDir(), "Movies")
	if err := d.updateCategory(ctx, cs[0].ID, CategoryInput{Name: "Кино", Layout: LayoutSeries, Folders: []string{movies}}); err != nil {
		t.Fatal(err)
	}
	cs, _ = d.categories(ctx)
	if cs[0].Name != "Фильмы" || cs[0].Layout != LayoutFilms || !cs[0].Kinopoisk || len(cs[0].Folders) != 1 || cs[0].Folders[0].Path != movies {
		t.Errorf("у стандартной меняются только папки: %+v", cs[0])
	}
}

// Своя категория: создать, изменить, удалить; одна папка — в одной категории; ручные переносы в
// удалённую категорию снимаются; устройства — включённые скрытые категории раздельно.
func TestOwnCategories(t *testing.T) {
	ctx := context.Background()
	d := openDB(t)
	root := t.TempDir()
	study := mkdir(t, root, "Study")
	home := mkdir(t, root, "Home")
	id, err := d.addCategory(ctx, CategoryInput{Name: "Обучение", Layout: LayoutSeries, Folders: []string{study}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.addCategory(ctx, CategoryInput{Name: "Ещё", Layout: LayoutFilms, Folders: []string{study + `\`}}); !errors.Is(err, ErrFolderTaken) {
		t.Errorf("та же папка во второй категории: %v", err)
	}
	up := filepath.Join(filepath.Dir(study), "STUDY")
	if _, err := d.addCategory(ctx, CategoryInput{Name: "Ещё", Layout: LayoutFilms, Folders: []string{up}}); !errors.Is(err, ErrFolderTaken) {
		t.Errorf("та же папка другим регистром: %v", err)
	}
	if err := d.updateCategory(ctx, id, CategoryInput{Name: "18+", Layout: LayoutFilms, Hidden: true, Folders: []string{home}}); err != nil {
		t.Fatal(err)
	}
	cs, _ := d.categories(ctx)
	c := cs[2]
	if c.Name != "18+" || c.Layout != LayoutFilms || c.Kinopoisk || !c.Hidden || c.Builtin != "" || len(c.Folders) != 1 || c.Folders[0].Path != home {
		t.Errorf("своя после правки: %+v", c)
	}
	if err := d.setDeviceCategory(ctx, "192.168.0.50", id, true); err != nil {
		t.Fatal(err)
	}
	on, _ := d.deviceCategories(ctx, "192.168.0.50")
	off, _ := d.deviceCategories(ctx, "pc")
	if !on[id] || off[id] {
		t.Errorf("скрытая на устройствах: телефон %v, пк %v", on, off)
	}
	if _, err := d.W.Exec(`INSERT INTO lib_cards (key, category) VALUES ('kp-1', ?)`, id); err != nil {
		t.Fatal(err)
	}
	if err := d.deleteCategory(ctx, id); err != nil {
		t.Fatal(err)
	}
	var cat any
	d.R.QueryRow(`SELECT category FROM lib_cards WHERE key = 'kp-1'`).Scan(&cat)
	if cs, _ := d.categories(ctx); len(cs) != 2 || cat != nil {
		t.Errorf("после удаления: категорий %d, перенос карточки %v", len(cs), cat)
	}
	if on, _ := d.deviceCategories(ctx, "192.168.0.50"); on[id] {
		t.Errorf("устройство помнит удалённую категорию")
	}
	if err := d.deleteCategory(ctx, 999); !errors.Is(err, ErrNoCategory) {
		t.Errorf("нет такой: %v", err)
	}
}

// Папка категории: локальный диск, есть, читается, не в папке загрузок.
func TestCheckFolder(t *testing.T) {
	root := t.TempDir()
	dl := mkdir(t, root, "Downloads")
	ok := mkdir(t, root, "Share")
	for _, c := range []struct {
		path string
		want error
	}{
		{ok, nil},
		{`\server\share\films`, ErrNotLocal},
		{`films`, ErrNotLocal},
		{filepath.Join(root, "нет"), ErrFolderMissing},
		{dl, ErrFolderInDownloads},
		{mkdir(t, dl, "Сериал"), ErrFolderInDownloads},
	} {
		if err := checkFolder(c.path, dl); !errors.Is(err, c.want) && !(c.want == nil && err == nil) {
			t.Errorf("%s: %v, нужно %v", c.path, err, c.want)
		}
	}
	file := filepath.Join(root, "film.mkv")
	os.WriteFile(file, nil, 0o644)
	if err := checkFolder(file, dl); !errors.Is(err, ErrFolderMissing) {
		t.Errorf("файл вместо папки: %v", err)
	}
}
