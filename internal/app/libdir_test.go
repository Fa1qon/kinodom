package app

import (
	"context"
	"fmt"
	"testing"

	"github.com/anacrolix/torrent/metainfo"

	"kinodom/internal/library"
)

// setLibFolder — папка стандартной категории медиатеки builtin («films», «series»).
func setLibFolder(t *testing.T, a *App, builtin, dir string) {
	t.Helper()
	cs, err := a.Library.Categories(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cs {
		if c.Builtin == builtin {
			in := library.CategoryInput{Name: c.Name, Layout: c.Layout, Kinopoisk: c.Kinopoisk, Hidden: c.Hidden, Folders: []string{dir}}
			if err := a.Library.UpdateCategory(context.Background(), c.ID, in); err != nil {
				t.Fatal(err)
			}
			return
		}
	}
	t.Fatalf("нет категории %s", builtin)
}

// download — «Скачать» раздачи id через API; папка, куда она качается.
func download(t *testing.T, a *App, id int64) string {
	t.Helper()
	var opened struct{ Hash string }
	postJSON(t, fmt.Sprintf("http://%s/api/v1/releases/%d/download", a.API.Addr(), id), struct{}{}, &opened)
	var ih metainfo.Hash
	if err := ih.FromHexString(opened.Hash); err != nil {
		t.Fatal(err)
	}
	return a.Torrents.Engine().TorrentDir(ih)
}

// filmRelease — раздача фикстуры Rutor («Динозавры [S01]» — сериал), переименованная в фильм.
func filmRelease(t *testing.T, a *App) int64 {
	t.Helper()
	id := rutorRelease(t, a)
	if _, err := a.DB.W.Exec(`UPDATE releases SET title = 'Фильм / Film (2026) WEB-DL 720p' WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	return id
}

// План 14В: «Скачать» фильма — в первую папку «Фильмов» медиатеки, сериала — «Сериалов»; папок нет — в папку
// загрузок; в папку нельзя писать — в папку загрузок (Review Focus 1).
func TestDownloadIntoLibraryFolders(t *testing.T) {
	t.Run("фильм", func(t *testing.T) {
		a := rutorApp(t)
		films := t.TempDir()
		setLibFolder(t, a, "films", films)
		setLibFolder(t, a, "series", t.TempDir())
		if got := download(t, a, filmRelease(t, a)); got != films {
			t.Fatalf("папка %q, нужно %q", got, films)
		}
	})
	t.Run("сериал", func(t *testing.T) {
		a := rutorApp(t)
		series := t.TempDir()
		setLibFolder(t, a, "films", t.TempDir())
		setLibFolder(t, a, "series", series)
		if got := download(t, a, rutorRelease(t, a)); got != series { // «Динозавры [S01]»
			t.Fatalf("папка %q, нужно %q", got, series)
		}
	})
	t.Run("папок нет", func(t *testing.T) {
		a := rutorApp(t)
		if got := download(t, a, rutorRelease(t, a)); got != a.Torrents.Engine().DownloadsDir() {
			t.Fatalf("папка %q", got)
		}
	})
	t.Run("писать нельзя", func(t *testing.T) {
		a := rutorApp(t)
		setLibFolder(t, a, "films", t.TempDir())
		a.writable = func(string) bool { return false }
		if got := download(t, a, filmRelease(t, a)); got != a.Torrents.Engine().DownloadsDir() {
			t.Fatalf("папка %q", got)
		}
	})
}

// Раздача, которая уже качалась в папку загрузок, после появления папок медиатеки остаётся там же
// (Review Focus 3).
func TestDownloadKeepsOldDir(t *testing.T) {
	a := rutorApp(t)
	id := filmRelease(t, a)
	first := download(t, a, id)
	setLibFolder(t, a, "films", t.TempDir())
	if got := download(t, a, id); got != first {
		t.Fatalf("раздачу перенесло: %q → %q", first, got)
	}
}
