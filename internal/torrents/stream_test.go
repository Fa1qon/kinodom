package torrents

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/anacrolix/torrent/metainfo"

	"kinodom/internal/power"
	"kinodom/internal/torrents/torrenttest"
)

// streamFixture — раздача с одним файлом, раздающий и HTTP-сервер с маршрутом потока.
func streamFixture(t *testing.T, name string, size int) (srv *httptest.Server, ih metainfo.Hash, want []byte) {
	t.Helper()
	_, srv, ih, want = streamFixtureService(t, name, size)
	return srv, ih, want
}

// streamFixtureService — то же, что streamFixture, плюс сам сервис (чтобы дотянуться до движка).
func streamFixtureService(t *testing.T, name string, size int) (s *Service, srv *httptest.Server, ih metainfo.Hash, want []byte) {
	t.Helper()
	return streamFixtureWith(t, name, size, nil)
}

// streamFixtureWith — то же, но setup настраивает сервис до запуска Run.
func streamFixtureWith(t *testing.T, name string, size int, setup func(*Service)) (s *Service, srv *httptest.Server, ih metainfo.Hash, want []byte) {
	t.Helper()
	s = newTestService(t)
	if setup != nil {
		setup(s)
	}
	runService(t, s)
	src := t.TempDir()
	mi, root := torrenttest.MakeTorrent(t, src, "Космос", 64<<10, torrenttest.File{Path: name, Size: size})
	want, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		t.Fatal(err)
	}
	seeder, _ := torrenttest.NewSeeder(t, src, mi)
	ih, err = s.Open(context.Background(), Source{Torrent: torrentBytes(t, mi)})
	if err != nil {
		t.Fatal(err)
	}
	connect(t, s, ih, seeder)
	if err := s.Prepare(context.Background(), ih, 0); err != nil { // поток — только хранимого файла
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("GET /stream/{hash}/{index}/{name}", s.StreamHandler())
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return s, srv, ih, want
}

func get(t *testing.T, url, rng string) (int, string, []byte) {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	if rng != "" {
		req.Header.Set("Range", rng)
	}
	// Без предела замерший поток повесил бы весь прогон на 10 минут (таймаут go test).
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, resp.Header.Get("Content-Type"), body
}

func TestStreamFullFileAndRangesWithCyrillicName(t *testing.T) {
	name := "Космос серия 1 (2014).mkv"
	srv, ih, want := streamFixture(t, name, 1<<20)
	u := srv.URL + streamPath(ih, 0, name)

	code, ct, body := get(t, u, "")
	if code != http.StatusOK || ct != "video/x-matroska" || !bytes.Equal(body, want) {
		t.Fatalf("целиком: код %d, тип %q, совпадает %v", code, ct, bytes.Equal(body, want))
	}
	for _, rg := range [][2]int{{0, 999}, {500_000, 500_999}, {len(want) - 1000, len(want) - 1}} {
		code, _, body := get(t, u, fmt.Sprintf("bytes=%d-%d", rg[0], rg[1]))
		if code != http.StatusPartialContent || !bytes.Equal(body, want[rg[0]:rg[1]+1]) {
			t.Fatalf("диапазон %v: код %d, байты совпадают %v", rg, code, bytes.Equal(body, want[rg[0]:rg[1]+1]))
		}
	}
}

func TestTwoViewersReadDifferentPlacesAtOnce(t *testing.T) {
	srv, ih, want := streamFixture(t, "film.mkv", 2<<20)
	u := srv.URL + streamPath(ih, 0, "film.mkv")
	var wg sync.WaitGroup
	errs := make(chan string, 2)
	for _, off := range []int{0, 1 << 20} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, _ := http.NewRequest("GET", u, nil)
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", off, off+300_000-1))
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				errs <- err.Error()
				return
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			if !bytes.Equal(body, want[off:off+300_000]) {
				errs <- fmt.Sprintf("зритель с %d получил не те байты", off)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
}

func TestStreamErrors(t *testing.T) {
	srv, ih, _ := streamFixture(t, "film.mkv", 100_000)
	cases := map[string]int{
		"/stream/zz/0/a.mkv":                              http.StatusBadRequest,
		"/stream/" + fmt.Sprintf("%040x", 1) + "/0/a.mkv": http.StatusNotFound,
		"/stream/" + ih.HexString() + "/9/a.mkv":          http.StatusNotFound,
	}
	for path, want := range cases {
		if code, _, _ := get(t, srv.URL+path, ""); code != want {
			t.Errorf("%s: код %d, ожидался %d", path, code, want)
		}
	}
}

// Соединение с единственным раздающим оборвалось. anacrolix сам к нему не переподключается:
// адрес вернут DHT, трекеры или PEX (в тестах — connect). Поток ждёт и продолжается.
func TestStreamResumesAfterOnlyPeerReconnects(t *testing.T) {
	s, srv, ih, want := streamFixtureService(t, "film.mkv", 2<<20)
	u := srv.URL + streamPath(ih, 0, "film.mkv")
	if code, _, body := get(t, u, "bytes=0-99999"); code != http.StatusPartialContent || !bytes.Equal(body, want[:100_000]) {
		t.Fatalf("начало: код %d", code)
	}
	tt, _ := s.Engine().Client().Torrent(ih)
	tt.SetMaxEstablishedConns(0) // рвёт все соединения — как если бы раздающий ушёл
	tt.SetMaxEstablishedConns(20)
	from := 1 << 20
	code, _, body := get(t, u, fmt.Sprintf("bytes=%d-%d", from, from+99_999))
	if code != http.StatusPartialContent || !bytes.Equal(body, want[from:from+100_000]) {
		t.Fatalf("после обрыва: код %d, байты совпадают %v", code, bytes.Equal(body, want[from:from+100_000]))
	}
}

// Пока идёт поток, ПК не засыпает; поток закрыт — счётчики потоков возвращаются к нулю (хвост
// этапа 2), а запрет сна держится ещё 10 минут.
func TestStreamHoldsPowerAndCountsBackToZero(t *testing.T) {
	k := power.New(quiet())
	t.Cleanup(func() { k.Close() })
	s, srv, ih, _ := streamFixtureWith(t, "film.mkv", 2<<20, func(s *Service) { s.UseKeeper(k) })
	resp, err := http.Get(srv.URL + "/stream/" + ih.HexString() + "/0/film.mkv")
	if err != nil {
		t.Fatal(err)
	}
	io.ReadFull(resp.Body, make([]byte, 64<<10))
	if k.Active() != 1 || s.ActiveStreams() != 1 {
		t.Fatalf("потоков: у запрета сна %d, у сервиса %d", k.Active(), s.ActiveStreams())
	}
	resp.Body.Close()
	for deadline := time.Now().Add(5 * time.Second); k.Active() != 0 || s.ActiveStreams() != 0; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("поток закрыт, а потоков: у запрета сна %d, у сервиса %d", k.Active(), s.ActiveStreams())
		}
	}
}

// Поток по старой ссылке к удалённому (нехранимому) файлу — 410 и без докачки (ревью этапа 6).
func TestStreamOfDeletedFileIsGone(t *testing.T) {
	s, srv, ih, _ := streamFixtureService(t, "film.mkv", 300_000)
	tt, _ := s.Engine().Client().Torrent(ih)
	waitComplete(t, tt.Files()[0])
	must(t, s.DeleteFile(context.Background(), ih, 0))
	code, _, _ := get(t, fmt.Sprintf("%s/stream/%s/0/film.mkv", srv.URL, ih.HexString()), "bytes=0-99")
	if code != http.StatusGone {
		t.Fatalf("код %d", code)
	}
	if p := tt.Files()[0].Priority(); p != 0 {
		t.Fatalf("удалённый файл снова качается: приоритет %v", p)
	}
}

// «Сейчас смотрят» пишется в базу не чаще раза в минуту на файл: Range-запросов у плеера десятки
// в минуту, а пишущее соединение с базой одно (хвост этапа 2).
func TestStreamTouchesDatabaseOncePerMinute(t *testing.T) {
	clk := &testClock{t: time.Now()}
	s, srv, ih, _ := streamFixtureWith(t, "film.mkv", 300_000, func(s *Service) { s.now = clk.now })
	url := fmt.Sprintf("%s/stream/%s/0/film.mkv", srv.URL, ih.HexString())
	lastStream := func() time.Time {
		f, _, err := s.reg.StoredFile(context.Background(), ih, 0)
		must(t, err)
		return f.LastStream
	}
	get(t, url, "bytes=0-99")
	first := lastStream()
	clk.add(10 * time.Second)
	get(t, url, "bytes=100-199")
	if !lastStream().Equal(first) {
		t.Fatal("через 10 секунд просмотр записан в базу снова")
	}
	clk.add(time.Minute)
	get(t, url, "bytes=200-299")
	if !lastStream().After(first) {
		t.Fatal("через минуту просмотр не записан")
	}
}
