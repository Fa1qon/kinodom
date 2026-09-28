package torrents

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"

	"kinodom/internal/torrents/torrenttest"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// newOfflineEngine — движок без сети; закрывается раньше, чем удаляются временные папки.
func newOfflineEngine(t *testing.T) *Engine {
	t.Helper()
	e, err := NewEngine(Config{DownloadsDir: t.TempDir(), StateDir: t.TempDir(), Offline: true, Log: quiet()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	return e
}

func newTestService(t *testing.T) *Service {
	t.Helper()
	db := newTestDB(t)
	return NewService(newOfflineEngine(t), NewRegistry(db), quiet())
}

// runService запускает Run сервиса; останавливается раньше, чем закрываются движок и база.
func runService(t *testing.T, s *Service) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
}

func waitStatus(t *testing.T, s *Service, ih metainfo.Hash, want TorrentState) TorrentStatus {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if st, ok := s.Status(ih); ok && st.State == want {
			return st
		}
		time.Sleep(20 * time.Millisecond)
	}
	st, _ := s.Status(ih)
	t.Fatalf("не дождались %s: %+v", want, st)
	return TorrentStatus{}
}

// connect подключает раздающего к раздаче сервиса (сети нет — пиров сообщаем вручную).
func connect(t *testing.T, s *Service, ih metainfo.Hash, seeder *torrent.Client) {
	t.Helper()
	tt, ok := s.Engine().Client().Torrent(ih)
	if !ok {
		t.Fatal("раздачи нет в движке")
	}
	torrenttest.Connect(t, tt, seeder)
}

func torrentBytes(t *testing.T, mi metainfo.MetaInfo) []byte {
	t.Helper()
	b, err := bencode.Marshal(mi)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func magnetOf(mi metainfo.MetaInfo) string {
	return "magnet:?xt=urn:btih:" + mi.HashInfoBytes().HexString()
}
