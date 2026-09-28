package torrents

import (
	"io"
	"os"
	"testing"
	"time"

	"github.com/anacrolix/torrent"

	"kinodom/internal/torrents/torrenttest"
)

func TestClassicFileIOIsOn(t *testing.T) {
	if got := os.Getenv("TORRENT_STORAGE_DEFAULT_FILE_IO"); got != "classic" {
		t.Fatalf("TORRENT_STORAGE_DEFAULT_FILE_IO = %q: пакет envfirst.local не сработал", got)
	}
}

// С mmap этот тест падает: файл остаётся занят до выхода процесса.
func TestFileOfLiveTorrentCanBeDeleted(t *testing.T) {
	dir := t.TempDir()
	mi, path := torrenttest.MakeTorrent(t, dir, "film.mkv", 32<<10, torrenttest.File{Path: "film.mkv", Size: 200_000})
	_, tt := torrenttest.NewSeeder(t, dir, mi)
	f := tt.Files()[0]
	r := f.NewReader()
	if _, err := io.ReadFull(r, make([]byte, 1000)); err != nil {
		t.Fatal(err)
	}
	r.Close()
	f.SetPriority(torrent.PiecePriorityNone)
	// Движок может ещё секунду-другую дочитывать куски (проверка после VerifyData), поэтому
	// удаление повторяется — как и в процедуре удаления этапа 6. С mmap файл занят до выхода
	// процесса, и никакие повторы не помогают.
	deadline := time.Now().Add(3 * time.Second)
	for {
		err := os.Remove(path)
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("файл живой раздачи не удаляется: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
