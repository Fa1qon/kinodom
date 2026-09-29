package torrents

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kinodom/internal/torrents/torrenttest"
)

func TestCheckDownloadsDir(t *testing.T) {
	ok := func(string, int64) error { return nil }
	fresh := filepath.Join(t.TempDir(), "Kinodom", "Загрузки")
	if err := checkDownloadsDirWith(fresh, ok); err != nil {
		t.Fatalf("новая папка: %v", err)
	}
	if entries, _ := os.ReadDir(fresh); len(entries) != 0 {
		t.Fatalf("проба не убрана: %v", entries)
	}
	file := filepath.Join(t.TempDir(), "файл.txt")
	must(t, os.WriteFile(file, []byte("x"), 0o644))
	cases := []struct {
		dir    string
		sparse func(string, int64) error
		want   string
	}{
		{`\nas\video\Kinodom`, ok, "на сетевом диске"},
		{`\?\UNC\nas\video`, ok, "на сетевом диске"},
		{filepath.Join(file, "Загрузки"), ok, "недоступна"},
		{t.TempDir(), func(string, int64) error { return errors.New("Incorrect function.") }, "без разрежённых файлов"},
	}
	for _, c := range cases {
		if err := checkDownloadsDirWith(c.dir, c.sparse); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, ожидалось «%s»", c.dir, err, c.want)
		}
	}
	if err := dirError(`D:\K`, fmt.Errorf("open: %w", fs.ErrPermission)); !strings.Contains(err.Error(), "нет права записи") {
		t.Errorf("нет прав: %v", err)
	}
	if isNetworkPath(`C:\Kinodom`) || isNetworkPath(`\?\C:\Kinodom`) {
		t.Error("локальный диск принят за сетевой")
	}
}

// Настройку папки загрузок сменили: старая раздача живёт в прежней папке (файлы на месте,
// скачанное не качается заново), новая — в новой (спека, раздел 9).
func TestTorrentStaysInItsDownloadsDir(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	state, oldDir, newDir := t.TempDir(), t.TempDir(), t.TempDir()
	src := t.TempDir()
	mi, _ := torrenttest.MakeTorrent(t, src, "film.mkv", 64<<10, torrenttest.File{Path: "film.mkv", Size: 300_000})
	seeder, _ := torrenttest.NewSeeder(t, src, mi)

	e1, err := NewEngine(Config{DownloadsDir: oldDir, StateDir: state, Offline: true, Log: quiet()})
	must(t, err)
	s1 := serviceFor(e1, NewRegistry(db))
	ih, err := s1.Open(ctx, Source{Torrent: torrentBytes(t, mi)})
	must(t, err)
	connect(t, s1, ih, seeder)
	must(t, s1.Prepare(ctx, ih, 0))
	tt, _ := e1.Client().Torrent(ih)
	for deadline := time.Now().Add(15 * time.Second); tt.BytesCompleted() < tt.Length(); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("файл не скачался")
		}
	}
	e1.Close()

	e2, err := NewEngine(Config{DownloadsDir: newDir, StateDir: state, Offline: true, Log: quiet()})
	must(t, err)
	t.Cleanup(func() { e2.Close() })
	s2 := serviceFor(e2, NewRegistry(db))
	runService(t, s2)
	waitStatus(t, s2, ih, StateReady)
	tt2, _ := e2.Client().Torrent(ih)
	// Пиров у второго движка нет: файл может стать полным, только если найден на диске. Ждём, а не
	// смотрим сразу — под нагрузкой (весь пакет) кусок изредка догонял через доли секунды.
	for deadline := time.Now().Add(5 * time.Second); tt2.BytesCompleted() < tt2.Length(); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("после смены папки скачано %d из %d — файл ищут не там", tt2.BytesCompleted(), tt2.Length())
		}
	}
	if e2.TorrentDir(ih) != oldDir {
		t.Fatalf("папка раздачи %s, ожидалась прежняя %s", e2.TorrentDir(ih), oldDir)
	}
	other, _ := torrenttest.MakeTorrent(t, t.TempDir(), "new.mkv", 64<<10, torrenttest.File{Path: "new.mkv", Size: 100_000})
	ih2, err := s2.Open(ctx, Source{Torrent: torrentBytes(t, other)})
	must(t, err)
	if e2.TorrentDir(ih2) != newDir {
		t.Fatalf("новая раздача в %s, ожидалась %s", e2.TorrentDir(ih2), newDir)
	}
}

// Диск с загрузками не подключён: записи не удаляются, в «Состоянии» — какая папка недоступна;
// диск вернулся — раздача возвращается без перезапуска.
func TestUnavailableDirKeepsRecords(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	reg := NewRegistry(db)
	mi, _ := torrenttest.MakeTorrent(t, t.TempDir(), "film.mkv", 64<<10, torrenttest.File{Path: "film.mkv", Size: 300_000})
	ih := mi.HashInfoBytes()
	usb := filepath.Join(t.TempDir(), "USB")
	if _, err := reg.Remember(ctx, ih, "torrent-file", usb); err != nil {
		t.Fatal(err)
	}
	must(t, reg.SaveMetainfo(ctx, ih, "film.mkv", torrentBytes(t, mi)))
	must(t, reg.MarkStored(ctx, ih, 0, filepath.Join(usb, "film.mkv"), 300_000, time.Now()))

	s := serviceFor(newOfflineEngine(t), reg)
	must(t, s.restore(ctx))
	if _, ok := s.Engine().Client().Torrent(ih); ok {
		t.Fatal("раздача с недоступного диска добавлена в движок")
	}
	if p := problemText(t, db, "torrents.dirs"); !strings.Contains(p, usb) {
		t.Fatalf("проблема %q", p)
	}
	if idx, _ := reg.StoredFiles(ctx, ih); len(idx) != 1 {
		t.Fatal("запись о файле удалена")
	}
	must(t, os.MkdirAll(usb, 0o755))
	must(t, s.restore(ctx))
	if _, ok := s.Engine().Client().Torrent(ih); !ok {
		t.Fatal("диск вернулся, а раздача — нет")
	}
	if p := problemText(t, db, "torrents.dirs"); p != "" {
		t.Fatalf("проблема не снята: %q", p)
	}
}
