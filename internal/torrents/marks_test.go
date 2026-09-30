package torrents

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anacrolix/torrent"
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
	must(t, reg.MarkStored(ctx, ih, 0, path, 16<<20))
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

// corruptRestart — фильм скачан и открыт 3 дня назад, файл отметок испорчен, движок запущен
// заново; места впритык. Возвращает второй сервис (без Run) и путь к фильму.
func corruptRestart(t *testing.T) (*Service, metainfo.Hash, string) {
	t.Helper()
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
	path := enginePath(down, t1.Info(), ih, t1.Info().UpvertedFiles()[0])
	s1.saveMetainfoNow(t, ih, mi)
	e1.Close()
	_, err = reg.db.W.Exec("UPDATE stored_files SET last_opened_at = ?", time.Now().Add(-72*time.Hour).UnixMilli())
	must(t, err)
	must(t, os.WriteFile(filepath.Join(state, marksFile), []byte("питание пропало"), 0o600))
	e2, err := NewEngine(Config{DownloadsDir: down, StateDir: state, Offline: true, Log: quiet()})
	must(t, err)
	t.Cleanup(func() { e2.Close() })
	s2 := serviceFor(e2, reg)
	// Свободно 10 МиБ при запасе 9,5: скачанному фильму ничего не нужно — удалять нечего.
	s2.freeSpace = func(string) (int64, error) { return 10 * mib, nil }
	s2.SetPolicy(Policy{MinFree: 9*mib + mib/2})
	return s2, ih, path
}

// Пока скачанное перепроверяется после повреждённого файла отметок, оно не «недокачанный остаток»:
// уборка не удаляет фильмы из-за мнимой нехватки места (ревью этапа 6, C1).
func TestReverificationDoesNotTriggerCleanup(t *testing.T) {
	s, ih, _ := corruptRestart(t)
	runService(t, s)
	tt := func() *torrent.Torrent { t2, _ := s.Engine().Client().Torrent(ih); return t2 }
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		if t2 := tt(); t2 != nil && t2.BytesCompleted() == t2.Length() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("фильм не перепроверился")
		}
	}
	if idx, _ := s.reg.StoredFiles(context.Background(), ih); len(idx) != 1 {
		t.Fatalf("скачанный фильм удалён уборкой во время перепроверки: хранимые %v", idx)
	}
}

// Файл удаляют, пока его куски ждут перепроверки: куски снимаются с очереди и освобождаются —
// потом они не «оживут» скачанными в файле, которого нет в базе.
func TestDeleteDuringReverificationFreesQueuedPieces(t *testing.T) {
	ctx := context.Background()
	s, ih, path := corruptRestart(t)
	must(t, s.restore(ctx))
	must(t, s.DeleteFile(ctx, ih, 0))
	s.verifySome(time.Second)
	t2, _ := s.Engine().Client().Torrent(ih)
	if t2 == nil {
		t.Fatal("раздача выгружена сразу — её сессию только что открыли")
	}
	if n := t2.BytesCompleted(); n != 0 {
		t.Fatalf("после удаления перепроверка отметила скачанными %d байт", n)
	}
	if a, err := allocatedSize(path); err != nil || a > 128<<10 {
		t.Fatalf("на диске осталось %d байт (%v)", a, err)
	}
}

// Повреждённая страница bolt, указывающая за пределы файла, — не паника, а сбой доступа к памяти;
// recover его не ловит без SetPanicOnFault, и служба падала бы по кругу (ревью этапа 6, I4).
func TestCheckMarksSurvivesMemoryFault(t *testing.T) {
	if p := os.Getenv("KD_FAULT_BOLT"); p != "" {
		re, err := checkMarks(filepath.Dir(p))
		fmt.Printf("recreated=%v err=%v\n", re, err)
		return
	}
	dir := t.TempDir()
	p := filepath.Join(dir, marksFile)
	db, err := bbolt.Open(p, 0o600, nil)
	must(t, err)
	must(t, db.Update(func(tx *bbolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists([]byte("completion"))
		if err != nil {
			return err
		}
		for i := range 5000 {
			if err := b.Put(binary.BigEndian.AppendUint32(nil, uint32(i)), make([]byte, 20)); err != nil {
				return err
			}
		}
		return nil
	}))
	ps := db.Info().PageSize
	must(t, db.Close())
	raw, err := os.ReadFile(p)
	must(t, err)
	patched := false
	for off := 0; off+ps <= len(raw) && !patched; off += ps {
		id := binary.LittleEndian.Uint64(raw[off:])
		flags := binary.LittleEndian.Uint16(raw[off+8:])
		count := binary.LittleEndian.Uint16(raw[off+10:])
		if id == uint64(off/ps) && flags == 0x01 && count > 0 { // страница-ветвь
			binary.LittleEndian.PutUint64(raw[off+16+8:], 1<<24) // потомок далеко за концом файла
			patched = true
		}
	}
	if !patched {
		t.Fatal("нет страницы-ветви")
	}
	must(t, os.WriteFile(p, raw, 0o600))
	cmd := exec.Command(os.Args[0], "-test.run", "^TestCheckMarksSurvivesMemoryFault$")
	cmd.Env = append(os.Environ(), "KD_FAULT_BOLT="+p)
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "recreated=true err=<nil>") {
		s := string(out)
		if i := strings.Index(s, "goroutine "); i > 0 {
			s = s[:i]
		}
		t.Fatalf("проверка файла отметок уронила процесс (%v):\n%s", err, s)
	}
}
