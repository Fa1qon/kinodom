package torrents

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"

	"kinodom/internal/torrents/torrenttest"
)

// Ловушка 4а опыта (исследование 22.1): файл на диске длиннее своей длины в раздаче — кусок на стыке со
// следующим файлом не сходится по хэшу никогда и перекачивается по кругу. prepStorage обрезает такой файл
// до длины в раздаче — раздача докачивается (спека 11b, 6.3.7).
func TestPrepStorageTruncatesLonger(t *testing.T) {
	s := newTestService(t)
	runService(t, s)
	src := t.TempDir()
	mi, _ := torrenttest.MakeTorrent(t, src, "Сериал", 64<<10,
		torrenttest.File{Path: "e01.mkv", Size: 300_000}, torrenttest.File{Path: "e02.mkv", Size: 200_000})
	seeder, _ := torrenttest.NewSeeder(t, src, mi)
	info, err := mi.UnmarshalInfo()
	if err != nil {
		t.Fatal(err)
	}
	ih := mi.HashInfoBytes()
	first := info.UpvertedFiles()[0]
	p := enginePath(s.Engine().DownloadsDir(), &info, ih, first)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, make([]byte, first.Length+50_000), 0o644); err != nil { // перезалитая серия длиннее
		t.Fatal(err)
	}
	if _, err := s.Open(context.Background(), Source{Torrent: torrentBytes(t, mi)}); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(p); err != nil || st.Size() != first.Length {
		t.Fatalf("длинный файл не обрезан: %v %v", st.Size(), err)
	}
	if err := s.Download(context.Background(), ih, nil); err != nil {
		t.Fatal(err)
	}
	connect(t, s, ih, seeder)
	tt, _ := s.Engine().Client().Torrent(ih)
	for _, f := range tt.Files() {
		waitComplete(t, f)
	}
}

// Отметки кусков раздач, которых больше нет в реестре (старая версия после перехода), удаляются при
// старте, до открытия bolt движком; отметки известных раздач — на месте (спека 11b, 6.3.7).
func TestForgetMarksKeepsKnown(t *testing.T) {
	dir := t.TempDir()
	known, gone := metainfo.Hash{1}, metainfo.Hash{2}
	pc, err := storage.NewBoltPieceCompletion(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, ih := range []metainfo.Hash{known, gone} {
		if err := pc.Set(metainfo.PieceKey{InfoHash: ih, Index: 0}, true); err != nil {
			t.Fatal(err)
		}
	}
	pc.Close()
	if err := forgetMarks(dir, map[metainfo.Hash]bool{known: true}); err != nil {
		t.Fatal(err)
	}
	pc, err = storage.NewBoltPieceCompletion(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	if c, _ := pc.Get(metainfo.PieceKey{InfoHash: known, Index: 0}); !c.Ok || !c.Complete {
		t.Fatalf("отметки известной раздачи пропали: %+v", c)
	}
	if c, _ := pc.Get(metainfo.PieceKey{InfoHash: gone, Index: 0}); c.Ok {
		t.Fatalf("отметки забытой раздачи остались: %+v", c)
	}
}

// Движок при создании чистит отметки по списку известных раздач (Config.KeepMarks).
func TestEngineForgetsUnknownMarks(t *testing.T) {
	state, dl := t.TempDir(), t.TempDir()
	gone := metainfo.Hash{3}
	pc, err := storage.NewBoltPieceCompletion(state)
	if err != nil {
		t.Fatal(err)
	}
	pc.Set(metainfo.PieceKey{InfoHash: gone, Index: 0}, true)
	pc.Close()
	e, err := NewEngine(Config{DownloadsDir: dl, StateDir: state, Offline: true, Log: quiet(),
		KeepMarks: func() ([]metainfo.Hash, error) { return nil, nil }})
	if err != nil {
		t.Fatal(err)
	}
	c, _ := e.pc.Get(metainfo.PieceKey{InfoHash: gone, Index: 0})
	e.Close()
	if c.Ok {
		t.Fatalf("отметки забытой раздачи остались: %+v", c)
	}
}
