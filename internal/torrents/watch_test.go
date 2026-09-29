package torrents

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anacrolix/torrent/metainfo"

	"kinodom/internal/torrents/torrenttest"
)

// fakeWatch — история просмотров для тестов.
type fakeWatch struct {
	mu      sync.Mutex
	reports []string // «устройство номер смещение/размер»
	dur     map[string]float64
	start   int
}

func (f *fakeWatch) Report(_ context.Context, device, hash string, index int, offset, size int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reports = append(f.reports, fmt.Sprintf("%s %d %d/%d", device, index, offset, size))
}

func (f *fakeWatch) Duration(_ context.Context, hash string, index int) (float64, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.dur[fmt.Sprint(hash, index)]
	return d, ok
}

func (f *fakeWatch) SetDuration(_ context.Context, hash string, index int, sec float64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dur[fmt.Sprint(hash, index)] = sec
	return nil
}

func (f *fakeWatch) StartSec(context.Context, string, string, int) int { return f.start }

func (f *fakeWatch) snapshot() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.reports...)
}

// quickWatch — отчёты без ожидания 10 с (тесты).
func quickWatch(t *testing.T, min time.Duration) {
	t.Helper()
	was, wasMin := watchEvery, watchMin
	watchEvery, watchMin = 20*time.Millisecond, min
	t.Cleanup(func() { watchEvery, watchMin = was, wasMin })
}

// Место по чтению потока (спека этапа 8, раздел 7.2): запрос, который шёл дольше watchMin, в конце
// сообщает, докуда дочитан файл; устройство — адрес запроса (этот ПК — «pc»).
func TestStreamReportsWatchPosition(t *testing.T) {
	quickWatch(t, 0)
	fw := &fakeWatch{dur: map[string]float64{}}
	_, srv, ih, want := streamFixtureWith(t, "film.mkv", 300_000, func(s *Service) { s.SetWatchTracker(fw) })
	resp, err := http.Get(srv.URL + "/stream/" + ih.HexString() + "/0/film.mkv")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !bytes.Equal(got, want) {
		t.Fatal("поток испорчен")
	}
	waitUntil(t, "отчёт о месте", func() bool {
		for _, r := range fw.snapshot() {
			if r == "pc 0 300000/300000" {
				return true
			}
		}
		return false
	})
}

// Короткий запрос (плеер при открытии читает индекс в конце файла) места не сообщает.
func TestShortStreamNotReported(t *testing.T) {
	quickWatch(t, time.Hour)
	fw := &fakeWatch{dur: map[string]float64{}}
	_, srv, ih, _ := streamFixtureWith(t, "film.mkv", 300_000, func(s *Service) { s.SetWatchTracker(fw) })
	req, _ := http.NewRequest("GET", srv.URL+"/stream/"+ih.HexString()+"/0/film.mkv", nil)
	req.Header.Set("Range", "bytes=-4096")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.ReadAll(resp.Body)
	resp.Body.Close()
	time.Sleep(100 * time.Millisecond)
	if r := fw.snapshot(); len(r) != 0 {
		t.Errorf("короткий запрос сообщил место: %v", r)
	}
}

// mp4Bytes — MP4 с moov в конце: mvhd на 7080 с.
func mp4Bytes() []byte {
	box := func(typ string, body []byte) []byte {
		out := make([]byte, 8)
		binary.BigEndian.PutUint32(out, uint32(8+len(body)))
		copy(out[4:], typ)
		return append(out, body...)
	}
	mvhd := make([]byte, 100)
	binary.BigEndian.PutUint32(mvhd[12:], 1000)
	binary.BigEndian.PutUint32(mvhd[16:], 7_080_000)
	var b bytes.Buffer
	b.Write(box("ftyp", []byte("isom0000")))
	b.Write(box("mdat", make([]byte, 200_000)))
	b.Write(box("moov", box("mvhd", mvhd)))
	return b.Bytes()
}

// Длительность файла — из заголовка, когда его начали смотреть (7.2).
func TestStreamLearnsDuration(t *testing.T) {
	quickWatch(t, time.Hour)
	fw := &fakeWatch{dur: map[string]float64{}}
	s := newTestService(t)
	s.SetWatchTracker(fw)
	runService(t, s)
	src := t.TempDir()
	data := mp4Bytes()
	mi, _ := torrenttest.MakeTorrent(t, src, "film.mp4", 16<<10, torrenttest.File{Path: "film.mp4", Data: data})
	seeder, _ := torrenttest.NewSeeder(t, src, mi)
	ih, err := s.Open(context.Background(), Source{Torrent: torrentBytes(t, mi)})
	if err != nil {
		t.Fatal(err)
	}
	connect(t, s, ih, seeder)
	if err := s.Prepare(context.Background(), ih, 0); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("GET /stream/{hash}/{index}/{name}", s.StreamHandler())
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	resp, err := http.Get(srv.URL + "/stream/" + ih.HexString() + "/0/film.mp4")
	if err != nil {
		t.Fatal(err)
	}
	io.ReadAll(resp.Body)
	resp.Body.Close()
	waitUntil(t, "длительность из заголовка", func() bool {
		d, ok := fw.Duration(context.Background(), ih.HexString(), 0)
		return ok && d == 7080
	})
	_ = os.Remove(filepath.Join(src, "film.mp4"))
}

// «Смотреть» продолжает с места (7.3): startSec в ответе, start в ссылке kinodom:// и start-time в
// .m3u8; «С начала» — с нуля.
func TestWatchStartsFromPosition(t *testing.T) {
	fw := &fakeWatch{dur: map[string]float64{}, start: 1790}
	s, _, ih, _ := streamFixtureWith(t, "film.mkv", 100_000, func(s *Service) { s.SetWatchTracker(fw) })
	mux := http.NewServeMux()
	mux.Handle("POST /watch/{hash}/{index}", http.HandlerFunc(s.handleWatch))
	mux.Handle("GET /m3u/{hash}/{file}", http.HandlerFunc(s.handleM3U))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	watch := func(body string) (out struct {
		StartSec  int     `json:"startSec"`
		M3UURL    string  `json:"m3uUrl"`
		LaunchURL *string `json:"launchUrl"`
	}) {
		resp, err := http.Post(srv.URL+"/watch/"+ih.HexString()+"/0", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			b, _ := io.ReadAll(resp.Body)
			t.Fatalf("watch: %d %s", resp.StatusCode, b)
		}
		json.NewDecoder(resp.Body).Decode(&out)
		return out
	}
	w := watch("{}")
	if w.StartSec != 1790 || w.LaunchURL == nil || !strings.Contains(*w.LaunchURL, "start=1790") || !strings.HasSuffix(w.M3UURL, ".m3u8?start=1790") {
		t.Errorf("с места: %+v %v", w, *w.LaunchURL)
	}
	resp, err := http.Get(srv.URL + "/m3u/" + ih.HexString() + "/0.m3u8?start=1790")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(b), "#EXTVLCOPT:start-time=1790\n") {
		t.Errorf(".m3u8 с места: %s", b)
	}
	if w := watch(`{"fromStart":true}`); w.StartSec != 0 || strings.Contains(*w.LaunchURL, "start=") || strings.Contains(w.M3UURL, "start") {
		t.Errorf("с начала: %+v", w)
	}
	if w := watch(""); w.StartSec != 1790 {
		t.Errorf("пустое тело: %+v", w)
	}
}

var _ = metainfo.Hash{}

func waitUntil(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("не дождались: %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
