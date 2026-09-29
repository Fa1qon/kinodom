package torrents

import (
	"context"
	"testing"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
	"golang.org/x/time/rate"

	"kinodom/internal/netx"
	"kinodom/internal/power"
	"kinodom/internal/torrents/torrenttest"
)

// Пока идёт поток, отдача — четверть лимита из настроек; поток закончился — лимит прежний.
func TestUploadIsQuarteredWhileStreaming(t *testing.T) {
	e, err := NewEngine(Config{DownloadsDir: t.TempDir(), StateDir: t.TempDir(), Offline: true, UploadLimit: 4 << 20, Log: quiet()})
	must(t, err)
	t.Cleanup(func() { e.Close() })
	s := serviceFor(e, NewRegistry(newTestDB(t)))
	k := power.New(quiet())
	t.Cleanup(func() { k.Close() })
	s.UseKeeper(k)
	s.shapeUpload()
	if got := e.up.Limit(); got != rate.Limit(4<<20) {
		t.Fatalf("без потоков лимит %v", got)
	}
	release := k.Acquire()
	s.shapeUpload()
	if got := e.up.Limit(); got != rate.Limit(1<<20) {
		t.Fatalf("во время потока лимит %v, ожидалась четверть", got)
	}
	release()
	s.shapeUpload()
	if got := e.up.Limit(); got != rate.Limit(4<<20) {
		t.Fatalf("после потока лимит %v", got)
	}
}

// newLeecher — сторонний клиент без данных: пробует скачать раздачу у сервиса.
func newLeecher(t *testing.T, mi metainfo.MetaInfo) *torrent.Torrent {
	t.Helper()
	cfg := torrenttest.OfflineConfig(t.TempDir())
	port, err := netx.FreeTCPUDPPort("127.0.0.1")
	must(t, err)
	cfg.ListenPort = port
	cl, err := torrent.NewClient(cfg)
	must(t, err)
	t.Cleanup(func() { cl.Close() })
	tt, err := cl.AddTorrent(&mi)
	must(t, err)
	<-tt.GotInfo()
	tt.DownloadAll()
	return tt
}

// Раздаются только самые недавно открытые раздачи; остальные скачанные молчат — к ним не
// подключиться и у них не скачать (спека, раздел 9).
func TestOnlyFreshestTorrentsSeed(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)
	var mis []metainfo.MetaInfo
	var ihs []metainfo.Hash
	for _, name := range []string{"old.mkv", "new.mkv"} {
		src := t.TempDir()
		mi, _ := torrenttest.MakeTorrent(t, src, name, 64<<10, torrenttest.File{Path: name, Size: 300_000})
		seeder, _ := torrenttest.NewSeeder(t, src, mi)
		ih, err := s.Open(ctx, Source{Torrent: torrentBytes(t, mi)})
		must(t, err)
		connect(t, s, ih, seeder)
		must(t, s.Prepare(ctx, ih, 0))
		tt, _ := s.Engine().Client().Torrent(ih)
		waitComplete(t, tt.Files()[0])
		mis, ihs = append(mis, mi), append(ihs, ih)
	}
	// Третья раздача — старая, но ещё докачивается (раздающих нет): ей пиры нужны.
	busy, _ := torrenttest.MakeTorrent(t, t.TempDir(), "busy.mkv", 64<<10, torrenttest.File{Path: "busy.mkv", Size: 300_000})
	ihBusy, err := s.Open(ctx, Source{Torrent: torrentBytes(t, busy)})
	must(t, err)
	must(t, s.Prepare(ctx, ihBusy, 0))
	openedAgo(t, s, ihBusy, 0, 72*time.Hour)
	openedAgo(t, s, ihs[0], 0, 48*time.Hour)
	openedAgo(t, s, ihs[1], 0, 24*time.Hour)
	s.SetPolicy(Policy{MaxSeeding: 1})
	must(t, s.limitSeeding(ctx))
	s.mu.Lock()
	busyQuiet := s.sessions[ihBusy].quiet
	s.mu.Unlock()
	if busyQuiet {
		t.Fatal("докачивающаяся раздача замолчала")
	}

	fresh := newLeecher(t, mis[1])
	fresh.AddClientPeer(s.Engine().Client())
	for deadline := time.Now().Add(10 * time.Second); fresh.BytesCompleted() < fresh.Length(); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("свежую раздачу у сервиса не скачать")
		}
	}
	old := newLeecher(t, mis[0])
	old.AddClientPeer(s.Engine().Client())
	time.Sleep(time.Second)
	if n := old.BytesCompleted(); n != 0 {
		t.Fatalf("старая раздача раздаётся: скачано %d байт", n)
	}
	oldT, _ := s.Engine().Client().Torrent(ihs[0])
	if n := oldT.Stats().ActivePeers; n != 0 {
		t.Fatalf("у молчащей раздачи %d соединений", n)
	}
}

// В «молчащей» раздаче выбрали новую серию — раздача сразу снова с пирами, серия качается (ревью, I3).
func TestPrepareWakesQuietTorrent(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)
	ih, tt, _ := seriesFixture(t, s)
	one, two := fileIndex(t, tt, "Серия 1.mkv"), fileIndex(t, tt, "Серия 2.mkv")
	must(t, s.Prepare(ctx, ih, one))
	waitComplete(t, tt.Files()[one])
	openedAgo(t, s, ih, one, 3*24*time.Hour)
	ihA, epA := archive(t, s)
	must(t, s.Prepare(ctx, ihA, epA[0])) // более свежая раздача занимает единственное место
	s.SetPolicy(Policy{MaxSeeding: 1})
	must(t, s.limitSeeding(ctx))
	s.mu.Lock()
	q := s.sessions[ih].quiet
	s.mu.Unlock()
	if !q {
		t.Fatal("подготовка: сериал не молчит")
	}
	must(t, s.Prepare(ctx, ih, two))
	waitComplete(t, tt.Files()[two])
}
