package torrents

import (
	"os"
	"testing"

	g "github.com/anacrolix/generics"
	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"

	"kinodom/internal/torrents/torrenttest"
)

func TestPrepStorageCreatesSparseFilesAndPremarks(t *testing.T) {
	src := t.TempDir()
	mi, _ := torrenttest.MakeTorrent(t, src, "Сериал", 64<<10,
		torrenttest.File{Path: "Серия 1.mkv", Size: 300_000},
		torrenttest.File{Path: "Серия 2.mkv", Size: 200_000},
		torrenttest.File{Path: "info.nfo", Size: 1000})
	info, err := mi.UnmarshalInfo()
	if err != nil {
		t.Fatal(err)
	}
	ih := mi.HashInfoBytes()

	down := t.TempDir()
	pc, err := storage.NewBoltPieceCompletion(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	fc := storage.NewFileOpts(storage.NewFileClientOpts{
		ClientBaseDir:   down,
		TorrentDirMaker: torrentDir,
		FilePathMaker:   filePath,
		PieceCompletion: pc,
		UsePartFiles:    g.Some(false),
	})
	cfg := torrenttest.OfflineConfig(down)
	cfg.DefaultStorage = prepStorage{inner: fc, pc: pc, base: down}
	cl, err := torrent.NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { cl.Close(); fc.Close() }() // fc.Close закрывает и bolt

	tt, err := cl.AddTorrent(&mi)
	if err != nil {
		t.Fatal(err)
	}
	<-tt.GotInfo()

	for _, fi := range info.UpvertedFiles() {
		p := enginePath(down, &info, ih, fi)
		st, err := os.Stat(p)
		if err != nil {
			t.Fatalf("файл не создан: %v", err)
		}
		if st.Size() != fi.Length {
			t.Fatalf("%s: размер %d, ожидался %d", p, st.Size(), fi.Length)
		}
		if sp, err := isSparse(p); err != nil || !sp {
			t.Fatalf("%s не разрежённый: %v", p, err)
		}
		if a, err := allocatedSize(p); err != nil || a > 64<<10 {
			t.Fatalf("%s занимает на диске %d байт (%v) — место не должно выделяться заранее", p, a, err)
		}
	}
	for i := 0; i < info.NumPieces(); i++ {
		c, err := pc.Get(metainfo.PieceKey{InfoHash: ih, Index: i})
		if err != nil || !c.Ok || c.Complete {
			t.Fatalf("кусок %d: %+v, %v — ожидалось «известно, что не скачан»", i, c, err)
		}
	}
}
