package torrents

import (
	"context"
	"strings"
	"testing"
	"time"

	"kinodom/internal/supervisor"
	"kinodom/internal/torrents/torrenttest"
)

func TestOpenTorrentFileListsFilesImmediately(t *testing.T) {
	s := newTestService(t)
	mi, _ := torrenttest.MakeTorrent(t, t.TempDir(), "Сериал", 64<<10,
		torrenttest.File{Path: "Серия 10.mkv", Size: 400_000},
		torrenttest.File{Path: "Серия 2.mkv", Size: 400_000},
		torrenttest.File{Path: "info.nfo", Size: 100})
	ih, err := s.Open(context.Background(), Source{Torrent: torrentBytes(t, mi)})
	if err != nil {
		t.Fatal(err)
	}
	st, ok := s.Status(ih)
	if !ok || st.State != StateReady {
		t.Fatalf("%+v", st)
	}
	if len(st.Files) != 2 || st.Files[0].Name != "Серия 2.mkv" || st.Files[1].Name != "Серия 10.mkv" {
		t.Fatalf("файлы: %+v", st.Files)
	}
}

func TestOpenMagnetGetsFileListFromPeerAndSavesIt(t *testing.T) {
	s := newTestService(t)
	runService(t, s)
	src := t.TempDir()
	mi, _ := torrenttest.MakeTorrent(t, src, "film.mkv", 64<<10, torrenttest.File{Path: "film.mkv", Size: 500_000})
	seeder, _ := torrenttest.NewSeeder(t, src, mi)
	ih, err := s.Open(context.Background(), Source{Magnet: magnetOf(mi)})
	if err != nil {
		t.Fatal(err)
	}
	connect(t, s, ih, seeder)
	st := waitStatus(t, s, ih, StateReady)
	if len(st.Files) != 1 || st.Files[0].Name != "film.mkv" {
		t.Fatalf("файлы: %+v", st.Files)
	}
	// Метаинфо сохраняется для восстановления после перезапуска.
	deadline := time.Now().Add(5 * time.Second)
	for {
		var n int
		s.reg.db.R.QueryRow("SELECT COUNT(*) FROM torrents WHERE metainfo IS NOT NULL").Scan(&n)
		if n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("метаинфо не сохранилась")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestReopenReturnsSameTorrent(t *testing.T) {
	s := newTestService(t)
	mi, _ := torrenttest.MakeTorrent(t, t.TempDir(), "film.mkv", 64<<10, torrenttest.File{Path: "film.mkv", Size: 100_000})
	a, err := s.Open(context.Background(), Source{Torrent: torrentBytes(t, mi)})
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Open(context.Background(), Source{Magnet: magnetOf(mi)}) // второй телевизор
	if err != nil {
		t.Fatal(err)
	}
	if a != b || len(s.sessions) != 1 {
		t.Fatalf("раздачи %s и %s, сессий %d", a.HexString(), b.HexString(), len(s.sessions))
	}
}

func TestNoPeersBecomesErrorAndReopenStartsOver(t *testing.T) {
	s := newTestService(t)
	s.noPeersAfter = 200 * time.Millisecond
	magnet := "magnet:?xt=urn:btih:" + strings.Repeat("ab", 20)
	ih, err := s.Open(context.Background(), Source{Magnet: magnet})
	if err != nil {
		t.Fatal(err)
	}
	st := waitStatus(t, s, ih, StateError)
	if !strings.Contains(st.Error, "Нет раздающих") {
		t.Fatalf("текст ошибки: %q", st.Error)
	}
	if _, ok := s.Engine().Client().Torrent(ih); ok {
		t.Fatal("раздача с ошибкой должна быть убрана из движка")
	}
	if _, err := s.Open(context.Background(), Source{Magnet: magnet}); err != nil {
		t.Fatal(err)
	}
	if st, _ := s.Status(ih); st.State != StateConnecting {
		t.Fatalf("повторное открытие должно начаться заново: %+v", st)
	}
}

func TestOpenRejectsBadSources(t *testing.T) {
	s := newTestService(t)
	cases := map[string]Source{
		".torrent": {Torrent: []byte("мусор")},
		"источник": {},
		"magnet":   {Magnet: "magnet:?xt=bad"},
	}
	for want, src := range cases {
		if _, err := s.Open(context.Background(), src); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("ожидалась ошибка про %q, получено %v", want, err)
		}
	}
}

func TestBadMagnetErrorIsReadable(t *testing.T) {
	s := newTestService(t)
	for _, m := range []string{"magnet:?xt=bad", "magnet:?xt=urn:btih:" + strings.Repeat("0", 40)} {
		_, err := s.Open(context.Background(), Source{Magnet: m})
		if err == nil || strings.Contains(err.Error(), "<nil>") || !strings.Contains(err.Error(), "infohash") {
			t.Errorf("%s: непонятный текст ошибки %v", m, err)
		}
	}
}

func TestRestoreOpensStoredTorrentWithoutPeers(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	reg := NewRegistry(db)
	mi, _ := torrenttest.MakeTorrent(t, t.TempDir(), "film.mkv", 64<<10, torrenttest.File{Path: "film.mkv", Size: 300_000})
	ih := mi.HashInfoBytes()
	must(t, reg.SaveMetainfo(ctx, ih, "film.mkv", torrentBytes(t, mi)))
	must(t, reg.MarkStored(ctx, ih, 0, `D:\K\film.mkv`, 300_000, time.Now()))

	s := serviceFor(newOfflineEngine(t), reg)
	runService(t, s)
	st := waitStatus(t, s, ih, StateReady) // метаинфо из базы — пиры не нужны
	if len(st.Files) != 1 {
		t.Fatalf("%+v", st)
	}
}

// Под сторожем модуль становится «running» только после восстановления раздач (контракт Ready).
func TestServiceBecomesRunningUnderSupervisor(t *testing.T) {
	s := newTestService(t)
	sup := supervisor.New(quiet())
	sup.Add(s, true)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { sup.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	deadline := time.Now().Add(5 * time.Second)
	for !sup.IsRunning("torrents") {
		if time.Now().After(deadline) {
			t.Fatalf("модуль не стал running: %+v", sup.Status())
		}
		time.Sleep(10 * time.Millisecond)
	}
}
