package torrents

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"

	"kinodom/internal/torrents/torrenttest"
)

func waitFileReady(t *testing.T, s *Service, ih metainfo.Hash, index int) FileStatus {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if fs, ok := s.FileStatus(ih, index); ok && fs.State == FileReady {
			return fs
		}
		time.Sleep(20 * time.Millisecond)
	}
	fs, _ := s.FileStatus(ih, index)
	t.Fatalf("буфер не готов: %+v", fs)
	return FileStatus{}
}

func TestPrepareDownloadsOnlyChosenEpisode(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)
	runService(t, s)
	src := t.TempDir()
	mi, _ := torrenttest.MakeTorrent(t, src, "Сериал", 64<<10,
		torrenttest.File{Path: "Серия 1.mkv", Size: 2 << 20},
		torrenttest.File{Path: "Серия 2.mkv", Size: 2 << 20})
	seeder, _ := torrenttest.NewSeeder(t, src, mi)
	ih, err := s.Open(ctx, Source{Torrent: torrentBytes(t, mi)})
	if err != nil {
		t.Fatal(err)
	}
	connect(t, s, ih, seeder)
	st, _ := s.Status(ih)
	first, second := st.Files[0].Index, st.Files[1].Index // «Серия 1», «Серия 2»

	if err := s.Prepare(ctx, ih, second); err != nil {
		t.Fatal(err)
	}
	fs := waitFileReady(t, s, ih, second)
	if fs.BufferPercent != 100 || !strings.HasPrefix(fs.StreamPath, "/stream/"+ih.HexString()+"/") {
		t.Fatalf("%+v", fs)
	}
	tt, _ := s.Engine().Client().Torrent(ih)
	chosen, other := tt.Files()[second], tt.Files()[first]
	deadline := time.Now().Add(15 * time.Second)
	for chosen.BytesCompleted() < chosen.Length() {
		if time.Now().After(deadline) {
			t.Fatal("выбранная серия не докачалась целиком")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if other.BytesCompleted() > other.Length()/2 {
		t.Fatalf("соседняя серия качается без спроса: %d из %d", other.BytesCompleted(), other.Length())
	}
	idx, err := s.reg.StoredFiles(ctx, ih)
	if err != nil || !slices.Equal(idx, []int{second}) {
		t.Fatalf("хранимые файлы: %v, %v", idx, err)
	}
}

func TestPrepareErrors(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)
	var unknown metainfo.Hash
	if err := s.Prepare(ctx, unknown, 0); !errors.Is(err, ErrNotOpen) {
		t.Errorf("неоткрытая раздача: %v", err)
	}
	noInfo, _ := s.Open(ctx, Source{Magnet: "magnet:?xt=urn:btih:" + strings.Repeat("cd", 20)})
	if err := s.Prepare(ctx, noInfo, 0); !errors.Is(err, ErrNoInfo) {
		t.Errorf("без метаинфо: %v", err)
	}
	mi, _ := torrenttest.MakeTorrent(t, t.TempDir(), "film.mkv", 64<<10, torrenttest.File{Path: "film.mkv", Size: 100_000})
	ih, _ := s.Open(ctx, Source{Torrent: torrentBytes(t, mi)})
	if err := s.Prepare(ctx, ih, 5); !errors.Is(err, ErrNoSuchFile) {
		t.Errorf("нет такого файла: %v", err)
	}
	if _, ok := s.FileStatus(ih, 0); ok {
		t.Error("до prepare состояния файла нет")
	}
}

func TestPrepareTwiceFromTwoTVs(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)
	mi, _ := torrenttest.MakeTorrent(t, t.TempDir(), "film.mkv", 64<<10, torrenttest.File{Path: "film.mkv", Size: 100_000})
	ih, _ := s.Open(ctx, Source{Torrent: torrentBytes(t, mi)})
	for i := 0; i < 2; i++ {
		if err := s.Prepare(ctx, ih, 0); err != nil {
			t.Fatalf("prepare %d: %v", i+1, err)
		}
	}
	if _, ok := s.FileStatus(ih, 0); !ok {
		t.Fatal("нет состояния файла")
	}
}

// Серия 1 хранилась до перезапуска и докачивается дальше; выбор серии 2 переносит на неё фокус
// очереди, но серию 1 с хранения не снимает: она ждёт очереди и докачается следом (этап 7).
func TestPrepareAfterRestartKeepsStoredEpisodeQueued(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	reg := NewRegistry(db)
	mi, _ := torrenttest.MakeTorrent(t, t.TempDir(), "Сериал", 64<<10,
		torrenttest.File{Path: "Серия 1.mkv", Size: 300_000},
		torrenttest.File{Path: "Серия 2.mkv", Size: 300_000})
	ih := mi.HashInfoBytes()
	must(t, reg.SaveMetainfo(ctx, ih, "Сериал", torrentBytes(t, mi)))
	must(t, reg.MarkStored(ctx, ih, 0, `D:\K\1.mkv`, 300_000, time.Now()))

	s := serviceFor(newOfflineEngine(t), reg)
	runService(t, s)
	waitStatus(t, s, ih, StateReady)
	tt, _ := s.Engine().Client().Torrent(ih)
	if p := tt.Files()[0].Priority(); p != torrent.PiecePriorityNormal {
		t.Fatalf("после восстановления серия 1 не докачивается: приоритет %v", p)
	}
	if err := s.Prepare(ctx, ih, 1); err != nil {
		t.Fatal(err)
	}
	if p := tt.Files()[1].Priority(); p != torrent.PiecePriorityNormal {
		t.Fatalf("выбранная серия 2 не качается: приоритет %v", p)
	}
	st, _ := s.Status(ih)
	if st.Focus != 1 || !st.Files[0].Stored || !st.Files[0].Queued || tt.Files()[0].Priority() != torrent.PiecePriorityNone {
		t.Fatalf("серия 1 должна ждать очереди: фокус %d, %+v", st.Focus, st.Files[0])
	}
}

// Раздача из .torrent сразу знает список файлов, но если раздающих нет — буфер не наберётся
// никогда. Вместо вечного «Буферизация 0 %» человек должен увидеть «Нет раздающих».
func TestDeadTorrentFileReportsNoSeeders(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)
	s.noPeersAfter = 200 * time.Millisecond
	mi, _ := torrenttest.MakeTorrent(t, t.TempDir(), "film.mkv", 64<<10, torrenttest.File{Path: "film.mkv", Size: 1 << 20})
	ih, err := s.Open(ctx, Source{Torrent: torrentBytes(t, mi)})
	if err != nil {
		t.Fatal(err)
	}
	must(t, s.Prepare(ctx, ih, 0))
	deadline := time.Now().Add(5 * time.Second)
	for {
		fs, _ := s.FileStatus(ih, 0)
		if fs.State == FileError {
			if !strings.Contains(fs.Error, "Нет раздающих") {
				t.Fatalf("текст ошибки: %q", fs.Error)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("мёртвая раздача так и висит в буферизации: %+v", fs)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
