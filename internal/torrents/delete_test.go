package torrents

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"

	"kinodom/internal/torrents/torrenttest"
)

// fileIndex — номер файла раздачи по имени.
func fileIndex(t *testing.T, tt *torrent.Torrent, name string) int {
	t.Helper()
	for i, f := range tt.Files() {
		if baseName(f.DisplayPath()) == name {
			return i
		}
	}
	t.Fatalf("в раздаче нет %s", name)
	return -1
}

// waitComplete ждёт, пока файл скачан и все его куски проверены по хэшу (BytesCompleted
// считает и куски, которые ещё проверяются).
func waitComplete(t *testing.T, f *torrent.File) {
	t.Helper()
	done := func() bool {
		if f.BytesCompleted() < f.Length() {
			return false
		}
		for i := f.BeginPieceIndex(); i < f.EndPieceIndex(); i++ {
			if !f.Torrent().PieceState(i).Complete {
				return false
			}
		}
		return true
	}
	for deadline := time.Now().Add(15 * time.Second); !done(); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("%s не докачался: %d из %d", f.DisplayPath(), f.BytesCompleted(), f.Length())
		}
	}
}

// seriesFixture — открытая раздача из трёх серий с раздающим. Размеры не кратны куску
// (64 КиБ): куски на границах серий общие — как в жизни.
func seriesFixture(t *testing.T, s *Service) (ih metainfo.Hash, tt *torrent.Torrent, root string) {
	t.Helper()
	src := t.TempDir()
	mi, root := torrenttest.MakeTorrent(t, src, "Сериал", 64<<10,
		torrenttest.File{Path: "Серия 1.mkv", Size: 1_000_000},
		torrenttest.File{Path: "Серия 2.mkv", Size: 1_500_000},
		torrenttest.File{Path: "Серия 3.mkv", Size: 700_000})
	seeder, _ := torrenttest.NewSeeder(t, src, mi)
	ih, err := s.Open(context.Background(), Source{Torrent: torrentBytes(t, mi)})
	must(t, err)
	connect(t, s, ih, seeder)
	tt, _ = s.Engine().Client().Torrent(ih)
	return ih, tt, root
}

// Удаление серии не выгружает раздачу: соседняя серия докачивается и читается целиком, а
// удалённая не «воскресает» — от неё остаётся пустой разрежённый файл (спека, раздел 9).
func TestDeleteFileKeepsNeighbourDownloading(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)
	runService(t, s)
	ih, tt, root := seriesFixture(t, s)
	one, two := fileIndex(t, tt, "Серия 1.mkv"), fileIndex(t, tt, "Серия 2.mkv")
	must(t, s.Prepare(ctx, ih, one))
	waitComplete(t, tt.Files()[one])
	must(t, s.Prepare(ctx, ih, two))
	must(t, s.DeleteFile(ctx, ih, one))

	waitComplete(t, tt.Files()[two])
	r := tt.Files()[two].NewReader()
	defer r.Close()
	// Читатель файла anacrolix дочитывает до конца последнего куска — берём ровно длину файла
	// (поток так и делает: ServeContent отдаёт размер файла).
	got, err := io.ReadAll(io.LimitReader(r, tt.Files()[two].Length()))
	must(t, err)
	want, _ := os.ReadFile(filepath.Join(root, "Серия 2.mkv"))
	if !bytes.Equal(got, want) {
		t.Fatal("соседняя серия прочиталась не так, как у раздающего")
	}
	path := enginePath(s.Engine().TorrentDir(ih), tt.Info(), ih, tt.Info().UpvertedFiles()[one])
	time.Sleep(300 * time.Millisecond) // дать движку время «воскресить» файл, если он решит
	st, err := os.Stat(path)
	must(t, err)
	a, err := allocatedSize(path)
	must(t, err)
	if st.Size() != tt.Files()[one].Length() || a > 128<<10 {
		t.Fatalf("удалённая серия: размер %d, на диске %d байт — ожидался пустой разрежённый файл", st.Size(), a)
	}
	if idx, _ := s.reg.StoredFiles(ctx, ih); !slices.Equal(idx, []int{two}) {
		t.Fatalf("хранимые файлы %v", idx)
	}
	if _, ok := s.Status(ih); !ok {
		t.Fatal("раздача выгружена, хотя вторая серия хранится")
	}
}

// Файл, который смотрят (поток идёт или был за последние 6 часов), не удаляется.
func TestDeleteWatchedFileIsRefused(t *testing.T) {
	ctx := context.Background()
	clk := &testClock{t: time.Now()}
	s := newTestService(t)
	s.now = clk.now
	runService(t, s)
	src := t.TempDir()
	mi, _ := torrenttest.MakeTorrent(t, src, "film.mkv", 64<<10, torrenttest.File{Path: "film.mkv", Size: 8 << 20})
	seeder, _ := torrenttest.NewSeeder(t, src, mi)
	ih, err := s.Open(ctx, Source{Torrent: torrentBytes(t, mi)})
	must(t, err)
	connect(t, s, ih, seeder)
	must(t, s.Prepare(ctx, ih, 0))
	tt, _ := s.Engine().Client().Torrent(ih)
	waitComplete(t, tt.Files()[0])
	mux := http.NewServeMux()
	mux.Handle("GET /stream/{hash}/{index}/{name}", s.StreamHandler())
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	// Телевизор открыл поток и держит его.
	resp, err := http.Get(srv.URL + "/stream/" + ih.HexString() + "/0/film.mkv")
	must(t, err)
	io.ReadFull(resp.Body, make([]byte, 1000))
	if err := s.DeleteFile(ctx, ih, 0); !errors.Is(err, ErrWatching) {
		t.Fatalf("поток открыт: %v", err)
	}
	clk.add(7 * time.Hour) // начало потока — давно, но поток всё ещё открыт
	if err := s.DeleteFile(ctx, ih, 0); !errors.Is(err, ErrWatching) {
		t.Fatalf("поток открыт 7 часов: %v", err)
	}
	resp.Body.Close()
	for deadline := time.Now().Add(5 * time.Second); s.readersOf(ih, 0) > 0; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("поток не закрылся")
		}
	}
	must(t, s.DeleteFile(ctx, ih, 0))
}

// Удалён последний хранимый файл — раздача уходит из движка, из базы и с диска, как только
// о ней перестали спрашивать.
func TestDeleteLastFileForgetsTorrent(t *testing.T) {
	ctx := context.Background()
	clk := &testClock{t: time.Now()}
	s := newTestService(t)
	s.now = clk.now
	runService(t, s)
	ih, tt, _ := seriesFixture(t, s)
	one := fileIndex(t, tt, "Серия 1.mkv")
	must(t, s.Prepare(ctx, ih, one))
	waitComplete(t, tt.Files()[one])
	dir := torrentDir(s.Engine().TorrentDir(ih), tt.Info(), ih)
	must(t, s.DeleteFile(ctx, ih, one))
	// Раздачу только что листали — сразу её не выгружают (ревью этапа 6, I2).
	if _, ok := s.Engine().Client().Torrent(ih); !ok {
		t.Fatal("раздача, о которой только что спрашивали, выгружена сразу")
	}
	clk.add(idleFor + time.Minute)
	must(t, s.sweep(ctx))
	if _, ok := s.Status(ih); ok {
		t.Fatal("раздача осталась открытой")
	}
	if _, ok := s.Engine().Client().Torrent(ih); ok {
		t.Fatal("раздача осталась в движке")
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("папка раздачи осталась: %v", err)
	}
	var n int
	s.reg.db.R.QueryRow("SELECT COUNT(*) FROM torrents").Scan(&n)
	if n != 0 {
		t.Fatalf("записей о раздачах %d", n)
	}
	if err := s.DeleteFile(ctx, ih, one); !errors.Is(err, ErrNotStored) {
		t.Fatalf("повторное удаление: %v", err)
	}
}

func TestDeleteRoute(t *testing.T) {
	ctx := context.Background()
	s, srv := apiFixture(t)
	ih, tt, _ := seriesFixture(t, s)
	one := fileIndex(t, tt, "Серия 1.mkv")
	url := srv.URL + "/api/v1/downloads/" + ih.HexString() + "/"
	var e struct{ Error string }
	if code := call(t, "DELETE", url+"0", nil, &e); code != http.StatusNotFound || e.Error != ErrNotStored.Error() {
		t.Fatalf("не скачан: %d %q", code, e.Error)
	}
	must(t, s.Prepare(ctx, ih, one))
	s.mu.Lock()
	s.sessions[ih].readers[one]++ // поток открыт: телевизор смотрит
	s.mu.Unlock()
	if code := call(t, "DELETE", url+strconv.Itoa(one), nil, &e); code != http.StatusConflict || e.Error != ErrWatching.Error() {
		t.Fatalf("смотрят: %d %q", code, e.Error)
	}
	s.mu.Lock()
	s.sessions[ih].readers[one]--
	s.mu.Unlock()
	// Смотрели только что, плеер закрыт — удаляется (замечание № 19, решение заказчика).
	must(t, s.reg.TouchStream(ctx, ih, one, time.Now()))
	if code := call(t, "DELETE", url+strconv.Itoa(one), nil, nil); code != http.StatusNoContent {
		t.Fatalf("удаление: %d", code)
	}
}

// Корзина раздачи в «Загрузках»: все хранимые серии удаляются, ту, что смотрят, — нет (спека этапа 7,
// раздел 10.6).
func TestDeleteReleaseRoute(t *testing.T) {
	ctx := context.Background()
	s, srv := apiFixture(t)
	ih, tt, _ := seriesFixture(t, s)
	one, two := fileIndex(t, tt, "Серия 1.mkv"), fileIndex(t, tt, "Серия 2.mkv")
	url := srv.URL + "/api/v1/downloads/" + ih.HexString()
	var e struct{ Error string }
	if code := call(t, "DELETE", url, nil, &e); code != http.StatusNotFound || e.Error != ErrNothingStored.Error() || !errors.Is(ErrNothingStored, ErrNotStored) {
		t.Fatalf("ничего не скачано: %d %q", code, e.Error)
	}
	must(t, s.Prepare(ctx, ih, one))
	must(t, s.Prepare(ctx, ih, two))
	must(t, s.reg.TouchStream(ctx, ih, one, time.Now())) // первую смотрели только что, плеер закрыт
	s.mu.Lock()
	s.sessions[ih].readers[two]++ // вторую смотрят сейчас
	s.mu.Unlock()
	var out struct{ Deleted, Skipped int }
	if code := call(t, "DELETE", url, nil, &out); code != http.StatusOK || out.Deleted != 1 || out.Skipped != 1 {
		t.Fatalf("удаление раздачи: %d %+v", code, out)
	}
	if left, err := s.reg.StoredFiles(ctx, ih); err != nil || !slices.Equal(left, []int{two}) {
		t.Fatalf("осталось %v, %v", left, err)
	}
}

// Кусок на границе с хранимой соседней серией при удалении не трогается: его байты нужны соседу, а
// сброс и повторная загрузка общего куска, который как раз качается, давали несошедшийся хэш — и
// anacrolix банил единственного раздающего (плавающее «не докачался», этап 6). Скачанные куски
// целиком внутри удалённой серии освобождаются.
func TestDeleteKeepsPieceSharedWithStoredNeighbour(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)
	ih, tt, _ := seriesFixture(t, s)
	one, two := fileIndex(t, tt, "Серия 1.mkv"), fileIndex(t, tt, "Серия 2.mkv")
	must(t, s.Prepare(ctx, ih, one))
	must(t, s.Prepare(ctx, ih, two))
	waitComplete(t, tt.Files()[one])
	waitComplete(t, tt.Files()[two])
	f := tt.Files()[one]
	shared, inner := f.EndPieceIndex()-1, f.BeginPieceIndex()
	must(t, s.DeleteFile(ctx, ih, one))
	if !tt.PieceState(shared).Complete {
		t.Fatalf("общий с соседней серией кусок %d сброшен", shared)
	}
	if tt.PieceState(inner).Complete {
		t.Fatalf("кусок %d удалённой серии всё ещё скачан", inner)
	}
}

// Замечание № 19 (спека 11b, 15.1): удаление человеком — мешает только открытый поток; поток 10 минут
// назад, плеер закрыт — удаляется. Корзина раздачи пропускает только серию с открытым потоком.
func TestDeleteRecentButNotStreaming(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)
	ih, ep := archive(t, s)
	must(t, s.Download(ctx, ih, []int{ep[0], ep[1], ep[2]}))
	must(t, s.reg.TouchStream(ctx, ih, ep[0], s.now().Add(-10*time.Minute)))
	if err := s.DeleteFile(ctx, ih, ep[0]); err != nil {
		t.Fatalf("смотрели 10 минут назад, плеер закрыт: %v", err)
	}
	s.mu.Lock()
	s.sessions[ih].readers[ep[1]]++
	s.mu.Unlock()
	if err := s.DeleteFile(ctx, ih, ep[1]); !errors.Is(err, ErrWatching) {
		t.Fatalf("поток открыт: %v", err)
	}
	must(t, s.reg.TouchStream(ctx, ih, ep[2], s.now().Add(-time.Hour)))
	deleted, skipped, err := s.DeleteRelease(ctx, ih)
	if err != nil || deleted != 1 || skipped != 1 {
		t.Fatalf("корзина раздачи: удалено %d, пропущено %d, %v", deleted, skipped, err)
	}
	if got := stored(t, s, ih); !slices.Equal(got, []int{ep[1]}) {
		t.Fatalf("осталось %v", got)
	}
}
