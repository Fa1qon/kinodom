package torrents

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kinodom/internal/torrents/torrenttest"
)

// План 14В: новая раздача качается в папку Source.Dir (папка медиатеки); знакомая остаётся, где была.
func TestOpenIntoDir(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)
	lib := t.TempDir()
	mi, _ := torrenttest.MakeTorrent(t, t.TempDir(), "Фильм", 64<<10, torrenttest.File{Path: "Фильм.mkv", Size: mib})
	ih, err := s.Open(ctx, Source{Torrent: torrentBytes(t, mi), Dir: lib})
	must(t, err)
	if got := s.Engine().TorrentDir(ih); got != lib {
		t.Fatalf("папка раздачи %q, нужно %q", got, lib)
	}
	if _, err := s.Open(ctx, Source{Torrent: torrentBytes(t, mi), Dir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	if got := s.Engine().TorrentDir(ih); got != lib {
		t.Fatalf("повторное открытие перенесло раздачу: %q", got)
	}
	fs, err := s.Folders(ctx)
	if err != nil || len(fs) != 1 || filepath.Dir(fs[0]) != lib || !strings.Contains(filepath.Base(fs[0]), "Фильм [") {
		t.Fatalf("папки раздач: %v, %v", fs, err)
	}
}

// «Удалять через» — и для скачанного в папку медиатеки: брошенная раздача удаляется, папка медиатеки остаётся.
func TestExpireInLibraryDir(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)
	lib := t.TempDir()
	mi, _ := torrenttest.MakeTorrent(t, t.TempDir(), "Брошенный", 64<<10,
		torrenttest.File{Path: "Серия 1.mkv", Size: mib}, torrenttest.File{Path: "Серия 2.mkv", Size: mib})
	ih, err := s.Open(ctx, Source{Torrent: torrentBytes(t, mi), Dir: lib})
	must(t, err)
	tt, _ := s.Engine().Client().Torrent(ih)
	a, b := fileIndex(t, tt, "Серия 1.mkv"), fileIndex(t, tt, "Серия 2.mkv")
	must(t, s.Prepare(ctx, ih, a))
	must(t, s.Prepare(ctx, ih, b))
	openedAgo(t, s, ih, a, 15*24*time.Hour)
	must(t, s.expire(ctx))
	if got := stored(t, s, ih); len(got) != 0 {
		t.Fatalf("хранятся %v", got)
	}
	if _, err := os.Stat(lib); err != nil {
		t.Fatalf("папка медиатеки: %v", err)
	}
}
