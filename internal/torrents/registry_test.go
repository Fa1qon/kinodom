package torrents

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/anacrolix/torrent/metainfo"

	"kinodom/internal/store"
)

func newTestDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func hashOf(t *testing.T, c string) metainfo.Hash {
	t.Helper()
	var ih metainfo.Hash
	if err := ih.FromHexString(strings.Repeat(c, 40)); err != nil {
		t.Fatal(err)
	}
	return ih
}

func TestRestorableNeedsMetainfoAndStoredFile(t *testing.T) {
	ctx := context.Background()
	r := NewRegistry(newTestDB(t))
	noMeta, noFiles, ok := hashOf(t, "a"), hashOf(t, "b"), hashOf(t, "c")
	now := time.Now()

	must(t, r.Remember(ctx, noMeta, "magnet:a"))
	must(t, r.MarkStored(ctx, noMeta, 0, `D:\K\a.mkv`, 1, now)) // файл есть, метаинфо нет
	must(t, r.SaveMetainfo(ctx, noFiles, "B", []byte("mi-b")))    // метаинфо есть, файлов нет
	must(t, r.SaveMetainfo(ctx, ok, "C", []byte("mi-c")))
	must(t, r.MarkStored(ctx, ok, 1, `D:\K\C\1.mkv`, 100, now))

	recs, err := r.Restorable(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].InfoHash != ok || recs[0].Name != "C" || string(recs[0].Metainfo) != "mi-c" {
		t.Fatalf("восстанавливать нужно только C: %+v", recs)
	}
	idx, err := r.StoredFiles(ctx, ok)
	if err != nil || !slices.Equal(idx, []int{1}) {
		t.Fatalf("хранимые файлы: %v, %v", idx, err)
	}
}

func TestSaveMetainfoKeepsSource(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	r := NewRegistry(db)
	ih := hashOf(t, "d")
	must(t, r.Remember(ctx, ih, "magnet:?xt=urn:btih:dd"))
	must(t, r.SaveMetainfo(ctx, ih, "D", []byte("mi")))
	var source string
	if err := db.R.QueryRow("SELECT source FROM torrents WHERE infohash = ?", ih.HexString()).Scan(&source); err != nil {
		t.Fatal(err)
	}
	if source != "magnet:?xt=urn:btih:dd" {
		t.Fatalf("источник затёрт: %q", source)
	}
}

func TestMarkStoredTwiceUpdatesOpenTime(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	r := NewRegistry(db)
	ih := hashOf(t, "e")
	must(t, r.Remember(ctx, ih, "x"))
	t1 := time.UnixMilli(1_000_000)
	t2 := time.UnixMilli(2_000_000)
	must(t, r.MarkStored(ctx, ih, 0, "p", 10, t1))
	must(t, r.MarkStored(ctx, ih, 0, "p", 10, t2)) // второй телевизор открыл ту же серию
	must(t, r.TouchStream(ctx, ih, 0, t2))
	var n int
	var opened, streamed int64
	if err := db.R.QueryRow("SELECT COUNT(*), MAX(last_opened_at), MAX(last_stream_at) FROM stored_files").Scan(&n, &opened, &streamed); err != nil {
		t.Fatal(err)
	}
	if n != 1 || opened != t2.UnixMilli() || streamed != t2.UnixMilli() {
		t.Fatalf("строк %d, открыт %d, поток %d", n, opened, streamed)
	}
}

func TestStoredFileNeedsKnownTorrent(t *testing.T) {
	r := NewRegistry(newTestDB(t))
	if err := r.MarkStored(context.Background(), hashOf(t, "f"), 0, "p", 1, time.Now()); err == nil {
		t.Fatal("файл неизвестной раздачи не должен записываться")
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
