package torrents

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/anacrolix/torrent/metainfo"
	"go.etcd.io/bbolt"

	"kinodom/internal/torrents/torrenttest"
)

// Цельный файл отметок не трогается; мусор и повреждённая страница — откладываются в сторону.
func TestCheckMarks(t *testing.T) {
	dir := t.TempDir()
	if re, err := checkMarks(dir); re || err != nil {
		t.Fatalf("файла нет: %v, %v", re, err)
	}
	p := filepath.Join(dir, marksFile)
	db, err := bbolt.Open(p, 0o600, nil)
	must(t, err)
	must(t, db.Update(func(tx *bbolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists([]byte("completion"))
		if err != nil {
			return err
		}
		for i := range 5000 { // несколько страниц
			k := binary.BigEndian.AppendUint32(nil, uint32(i))
			if err := b.Put(k, bytes.Repeat([]byte{1}, 20)); err != nil {
				return err
			}
		}
		return nil
	}))
	must(t, db.Close())
	if re, err := checkMarks(dir); re || err != nil {
		t.Fatalf("цельный файл: %v, %v", re, err)
	}

	// Середина файла затёрта — как после отключения питания посреди записи.
	raw, err := os.ReadFile(p)
	must(t, err)
	for i := len(raw) / 3; i < len(raw)/3+8192 && i < len(raw); i++ {
		raw[i] = 0xFF
	}
	must(t, os.WriteFile(p, raw, 0o600))
	if re, err := checkMarks(dir); !re || err != nil {
		t.Fatalf("повреждённая страница: %v, %v", re, err)
	}
	if _, err := os.Stat(p + ".corrupt"); err != nil {
		t.Fatalf("повреждённый файл не отложен: %v", err)
	}
	must(t, os.WriteFile(p, []byte("это не bolt"), 0o600))
	if re, err := checkMarks(dir); !re || err != nil {
		t.Fatalf("мусор: %v, %v", re, err)
	}
}

// Файл отметок повреждён — движок стартует с новым, а скачанное находится перепроверкой по хэшам,
// без пиров и без повторной загрузки (спека, раздел 9).
func TestCorruptMarksAreRecreatedAndFilesReverified(t *testing.T) {
	ctx := context.Background()
	reg := NewRegistry(newTestDB(t))
	state, down, src := t.TempDir(), t.TempDir(), t.TempDir()
	mi, _ := torrenttest.MakeTorrent(t, src, "film.mkv", 64<<10, torrenttest.File{Path: "film.mkv", Size: 1 << 20})
	seeder, _ := torrenttest.NewSeeder(t, src, mi)
	e1, err := NewEngine(Config{DownloadsDir: down, StateDir: state, Offline: true, Log: quiet()})
	must(t, err)
	s1 := serviceFor(e1, reg)
	ih, err := s1.Open(ctx, Source{Torrent: torrentBytes(t, mi)})
	must(t, err)
	connect(t, s1, ih, seeder)
	must(t, s1.Prepare(ctx, ih, 0))
	t1, _ := e1.Client().Torrent(ih)
	waitComplete(t, t1.Files()[0])
	s1.saveMetainfoNow(t, ih, mi)
	e1.Close()

	must(t, os.WriteFile(filepath.Join(state, marksFile), []byte("питание пропало"), 0o600))
	e2, err := NewEngine(Config{DownloadsDir: down, StateDir: state, Offline: true, Log: quiet()})
	must(t, err)
	t.Cleanup(func() { e2.Close() })
	s2 := serviceFor(e2, reg)
	runService(t, s2)
	waitStatus(t, s2, ih, StateReady)
	t2, _ := e2.Client().Torrent(ih)
	waitComplete(t, t2.Files()[0]) // пиров нет: скачанное может найтись только перепроверкой
}

// saveMetainfoNow дожидается, пока метаинфо раздачи окажется в базе (для перезапуска).
func (s *Service) saveMetainfoNow(t *testing.T, ih metainfo.Hash, mi metainfo.MetaInfo) {
	t.Helper()
	must(t, s.reg.SaveMetainfo(context.Background(), ih, "film.mkv", torrentBytes(t, mi)))
}

// Перезапуск с частично скачанным файлом: скачанное начало отдаётся по Range без пиров (хвост
// этапа 2).
func TestRestartServesDownloadedRangeWithoutPeers(t *testing.T) {
	ctx := context.Background()
	reg := NewRegistry(newTestDB(t))
	state, down, src := t.TempDir(), t.TempDir(), t.TempDir()
	mi, root := torrenttest.MakeTorrent(t, src, "film.mkv", 64<<10, torrenttest.File{Path: "film.mkv", Size: 16 << 20})
	want, _ := os.ReadFile(filepath.Join(root))
	seeder, _ := torrenttest.NewSeeder(t, src, mi)
	e1, err := NewEngine(Config{DownloadsDir: down, StateDir: state, Offline: true, Log: quiet()})
	must(t, err)
	s1 := serviceFor(e1, reg)
	ih, err := s1.Open(ctx, Source{Torrent: torrentBytes(t, mi)})
	must(t, err)
	connect(t, s1, ih, seeder)
	t1, _ := e1.Client().Torrent(ih)
	r := t1.Files()[0].NewReader()
	r.SetReadahead(0)
	if _, err := io.ReadFull(r, make([]byte, 256<<10)); err != nil {
		t.Fatal(err)
	}
	r.Close()
	path := enginePath(down, t1.Info(), ih, t1.Info().UpvertedFiles()[0])
	must(t, reg.MarkStored(ctx, ih, 0, path, 16<<20, time.Now()))
	s1.saveMetainfoNow(t, ih, mi)
	e1.Close()

	e2, err := NewEngine(Config{DownloadsDir: down, StateDir: state, Offline: true, Log: quiet()})
	must(t, err)
	t.Cleanup(func() { e2.Close() })
	s2 := serviceFor(e2, reg)
	runService(t, s2)
	waitStatus(t, s2, ih, StateReady)
	t2, _ := e2.Client().Torrent(ih)
	if t2.BytesCompleted() >= t2.Length() {
		t.Fatal("файл скачан целиком — проверка частичного файла ничего не проверяет")
	}
	mux := http.NewServeMux()
	mux.Handle("GET /stream/{hash}/{index}/{name}", s2.StreamHandler())
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	code, _, body := get(t, srv.URL+"/stream/"+ih.HexString()+"/0/film.mkv", "bytes=1000-99999")
	if code != http.StatusPartialContent || !bytes.Equal(body, want[1000:100000]) {
		t.Fatalf("код %d, байт %d", code, len(body))
	}
}
