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
	"kinodom/internal/watch"
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

// quickWatch — отчёты без ожидания 10 с (тесты); сеанс кончается через 300 мс без запросов.
func quickWatch(t *testing.T, min time.Duration) {
	t.Helper()
	was, wasMin, wasGap, wasLead, wasJump := watch.Every, watch.Min, watch.Gap, watch.Lead, watch.JumpRead
	watch.Every, watch.Min, watch.Gap, watch.Lead, watch.JumpRead = 20*time.Millisecond, min, 300*time.Millisecond, 0, 64<<10
	t.Cleanup(func() {
		watch.Every, watch.Min, watch.Gap, watch.Lead, watch.JumpRead = was, wasMin, wasGap, wasLead, wasJump
	})
}

// get — запрос потока с Range; тело читается целиком.
func getRange(t *testing.T, url, rng string) {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	if rng != "" {
		req.Header.Set("Range", rng)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.ReadAll(resp.Body)
	resp.Body.Close()
}

// Как VLC: поток — короткими запросами по очереди, каждый короче watchMin; место всё равно
// записывается — по сеансу. Прыжок в конец файла посреди сеанса (плеер читает индекс), который
// держится меньше watchMin, — не место, даже если отчёт пришёлся на него.
func TestShortRequestsLikeVLC(t *testing.T) {
	quickWatch(t, 1500*time.Millisecond)
	watch.Gap = 5 * time.Second // паузы между запросами короче конца сеанса, как у VLC (2 с против 30)
	fw := &fakeWatch{dur: map[string]float64{}}
	_, srv, ih, _ := streamFixtureWith(t, "film.avi", 300_000, func(s *Service) { s.SetWatchTracker(fw) })
	url := srv.URL + "/stream/" + ih.HexString() + "/0/film.avi"
	getRange(t, url, "bytes=0-19999")
	time.Sleep(1700 * time.Millisecond) // сеанс старше watchMin — место уже сообщается
	getRange(t, url, "bytes=-4096")     // индекс в конце файла
	time.Sleep(1200 * time.Millisecond) // отчёт приходится на чтение индекса
	for _, r := range []string{"bytes=20000-59999", "bytes=60000-99999", "bytes=100000-119999"} {
		time.Sleep(100 * time.Millisecond)
		getRange(t, url, r)
	}
	waitUntil(t, "место по сеансу", func() bool {
		r := fw.snapshot()
		return len(r) > 0 && r[len(r)-1] == "pc 0 120000/300000"
	})
	for _, r := range fw.snapshot() {
		if strings.HasSuffix(r, " 300000/300000") {
			t.Errorf("чтение индекса в конце записано как место: %v", fw.snapshot())
		}
	}
	if r := fw.snapshot(); len(r) == 0 || r[0] != "pc 0 20000/300000" {
		t.Errorf("первое место: %v", r)
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

// Открыл серию и сразу закрыл: последний запрос плеера — к индексу в конце файла. Это не место и не
// «просмотрено» (финальное ревью этапа 8): прыжок в конец принимается, только если оттуда прочитано
// заметно много.
func TestTailReadNotWatched(t *testing.T) {
	quickWatch(t, 0)
	fw := &fakeWatch{dur: map[string]float64{}}
	_, srv, ih, _ := streamFixtureWith(t, "film.mp4", 300_000, func(s *Service) { s.SetWatchTracker(fw) })
	url := srv.URL + "/stream/" + ih.HexString() + "/0/film.mp4"
	getRange(t, url, "bytes=0-19999")
	getRange(t, url, "bytes=-4096")
	time.Sleep(2500 * time.Millisecond) // сеанс кончился, итог сообщён
	r := fw.snapshot()
	if len(r) == 0 {
		t.Fatal("место не сообщено")
	}
	for _, x := range r {
		if !strings.HasPrefix(x, "pc 0 ") || strings.HasSuffix(x, " 300000/300000") || strings.Contains(x, " 29") {
			t.Errorf("индекс в конце стал местом: %v", r)
			break
		}
	}
}
