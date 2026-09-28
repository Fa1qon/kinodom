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

	"github.com/anacrolix/torrent/metainfo"

	"kinodom/internal/torrents/torrenttest"
)

// streamFixture — раздача с одним файлом, раздающий и HTTP-сервер с маршрутом потока.
func streamFixture(t *testing.T, name string, size int) (srv *httptest.Server, ih metainfo.Hash, want []byte) {
	t.Helper()
	s := newTestService(t)
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
	mux := http.NewServeMux()
	mux.Handle("GET /stream/{hash}/{index}/{name}", s.StreamHandler())
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, ih, want
}

func get(t *testing.T, url, rng string) (int, string, []byte) {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	if rng != "" {
		req.Header.Set("Range", rng)
	}
	resp, err := http.DefaultClient.Do(req)
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
