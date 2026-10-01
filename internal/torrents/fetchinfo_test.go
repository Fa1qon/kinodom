package torrents

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"testing"
	"time"

	"github.com/anacrolix/torrent/metainfo"

	"kinodom/internal/torrents/torrenttest"
)

// filesUnder — файлы в папке (рекурсивно).
func filesUnder(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			out = append(out, p)
		}
		return nil
	})
	return out
}

// Метаинфо новой версии раздачи по magnet — от раздающего, во временной раздаче без хранилища: на диске
// ничего не появляется, в движке после ответа раздачи нет (спека 11b, 6.3.1).
func TestFetchInfoFromSeeder(t *testing.T) {
	s := newTestService(t)
	src := t.TempDir()
	mi, _ := torrenttest.MakeTorrent(t, src, "Сериал", 64<<10,
		torrenttest.File{Path: "e01.mkv", Size: 300 << 10}, torrenttest.File{Path: "e02.mkv", Size: 200 << 10})
	seeder, _ := torrenttest.NewSeeder(t, src, mi)
	ih := mi.HashInfoBytes()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	type result struct {
		raw []byte
		err error
	}
	done := make(chan result, 1)
	go func() {
		raw, err := s.FetchInfo(ctx, magnetOf(mi))
		done <- result{raw, err}
	}()
	waitFor(t, "временная раздача в движке", func() bool { _, ok := s.Engine().Client().Torrent(ih); return ok })
	connect(t, s, ih, seeder)
	r := <-done
	if r.err != nil {
		t.Fatal(r.err)
	}
	got, err := metainfo.Load(bytes.NewReader(r.raw))
	if err != nil {
		t.Fatal(err)
	}
	info, err := got.UnmarshalInfo()
	if err != nil || got.HashInfoBytes() != ih || len(info.Files) != 2 {
		t.Fatalf("метаинфо: %v, файлов %d", err, len(info.Files))
	}
	if fs := filesUnder(t, s.Engine().DownloadsDir()); len(fs) != 0 {
		t.Fatalf("на диске появились файлы: %v", fs)
	}
	if _, ok := s.Engine().Client().Torrent(ih); ok {
		t.Fatal("временная раздача осталась в движке")
	}
}

// Пиров нет — ошибка по ctx, временная раздача убрана.
func TestFetchInfoTimeout(t *testing.T) {
	s := newTestService(t)
	mi, _ := torrenttest.MakeTorrent(t, t.TempDir(), "Нет пиров", 64<<10, torrenttest.File{Path: "e01.mkv", Size: 100 << 10})
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, err := s.FetchInfo(ctx, magnetOf(mi))
	if !errors.Is(err, ErrNoInfo) {
		t.Fatalf("нужна ErrNoInfo: %v", err)
	}
	if _, ok := s.Engine().Client().Torrent(mi.HashInfoBytes()); ok {
		t.Fatal("временная раздача осталась в движке")
	}
}
