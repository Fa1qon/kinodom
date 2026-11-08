package torrents

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
	"golang.org/x/time/rate"

	"kinodom/internal/torrents/torrenttest"
)

func TestPreparedDownloadWindowMovesAndYieldsToReader(t *testing.T) {
	s := newTestService(t)
	src := t.TempDir()
	mi, _ := torrenttest.MakeTorrent(t, src, "film.mkv", 64<<10, torrenttest.File{Path: "film.mkv", Size: 32 << 20})
	limit := rate.NewLimiter(2<<20, 16<<10)
	seeder, _ := torrenttest.NewSeeder(t, src, mi, func(cfg *torrent.ClientConfig) { cfg.UploadRateLimiter = limit })
	ih, err := s.Open(context.Background(), Source{Torrent: torrentBytes(t, mi)})
	if err != nil {
		t.Fatal(err)
	}
	must(t, s.Prepare(context.Background(), ih, 0))
	tt, _ := s.eng.cl.Torrent(ih)
	connect(t, s, ih, seeder)
	deadline := time.Now().Add(20 * time.Second)
	for {
		ready := true
		for i := 0; i < minHead/(64<<10); i++ {
			if !tt.PieceState(i).Complete {
				ready = false
				break
			}
		}
		if ready {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("не скачано непрерывное начало")
		}
		time.Sleep(20 * time.Millisecond)
	}
	limit.SetLimit(0) // сохраняем недокачанный файл для проверки продвижения окна
	s.sample()
	s.mu.Lock()
	ss := s.sessions[ih]
	first := tt.Files()[0].BeginPieceIndex()
	for first < tt.Files()[0].EndPieceIndex() && tt.PieceState(first).Complete {
		first++
	}
	if first >= ss.prepared[0].tail.begin || ss.playPriorities[first] != torrent.PiecePriorityHigh {
		s.mu.Unlock()
		t.Fatal("окно не перешло на недостающий участок после начала файла")
	}
	s.mu.Unlock()
	closeReader := s.openReader(ih, 0)
	// При просмотре окно задаёт позиция потока: ближайший недокачанный кусок — первым (Now), тело файла
	// ждёт, пока впереди нет запаса.
	if p := tt.PieceState(first).Priority; p != torrent.PiecePriorityNow {
		t.Fatalf("ближайший недокачанный кусок при просмотре — %v, ожидался Now", p)
	}
	s.mu.Lock()
	held := ss.held[0]
	s.mu.Unlock()
	if !held {
		t.Fatal("запаса впереди нет — тело файла должно ждать окна просмотра")
	}
	closeReader()
	s.mu.Lock()
	ss.paused[0] = true
	s.applyLocked(ss)
	s.mu.Unlock()
	if tt.PieceState(first).Priority != torrent.PiecePriorityNone || tt.PieceState(ss.prepared[0].tail.begin).Priority != torrent.PiecePriorityNone {
		t.Fatal("пауза должна снимать приоритеты тела и индекса")
	}
}

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

func TestFileStatusContainsPlaybackDiagnostics(t *testing.T) {
	s := newTestService(t)
	ih, ep := archive(t, s)
	if err := s.Prepare(context.Background(), ih, ep[0]); err != nil {
		t.Fatal(err)
	}
	fs, ok := s.FileStatus(ih, ep[0])
	if !ok {
		t.Fatal("нет статуса выбранного файла")
	}
	if fs.Total <= 0 || fs.Done < 0 || fs.Done > fs.Total || !fs.Focus {
		t.Fatalf("некорректная диагностика: %+v", fs)
	}
	if fs.HeadPercent < 0 || fs.HeadPercent > 100 || fs.TailPercent < 0 || fs.TailPercent > 100 {
		t.Fatalf("некорректный буфер: %+v", fs)
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
	stored := filepath.Join(t.TempDir(), "1.mkv")
	must(t, os.WriteFile(stored, make([]byte, 300_000), 0o644))
	must(t, reg.MarkStored(ctx, ih, 0, stored, 300_000))

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
