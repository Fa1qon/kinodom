package library

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"kinodom/internal/power"
	"kinodom/internal/watch"
)

// mediaMux — маршруты потока медиатеки на ServeMux.
func mediaMux(l *Library) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /media/{file}/{name}", l.handleMedia)
	mux.HandleFunc("GET /api/v1/library/files/{file}/play", l.handlePlay)
	mux.HandleFunc("GET /m3u/library/{file}", l.handleM3U)
	return mux
}

func get(t *testing.T, mux http.Handler, url, from string, hdr ...string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("GET", url, nil)
	r.RemoteAddr = from
	r.Host = "127.0.0.1:8090"
	for i := 0; i+1 < len(hdr); i += 2 {
		r.Header.Set(hdr[i], hdr[i+1])
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

const (
	fromPC    = "127.0.0.1:5000"
	fromPhone = "192.168.0.50:5000"
)

// withFile — медиатека с одной папкой «Фильмы» и файлом с данными; номер файла и единицы.
func withFile(t *testing.T, name string, data []byte) (*env, int64, int64, string) {
	t.Helper()
	e := newEnv(t)
	dir := e.folder(t, catFilms, "Movies")
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	os.Chtimes(p, old, old)
	e.scan(t)
	var file, unit int64
	if err := e.d.R.QueryRow(`SELECT id, unit FROM lib_files`).Scan(&file, &unit); err != nil {
		t.Fatal(err)
	}
	return e, file, unit, p
}

func mediaURL(file int64, name string) string {
	return "/media/" + strconv.FormatInt(file, 10) + "/" + strings.ReplaceAll(name, " ", "%20")
}

// Range → 206 и нужные байты; чужой номер и файл раздачи — 404: путь из запроса не используется.
func TestMediaRange(t *testing.T) {
	data := []byte("0123456789abcdef")
	e, file, _, _ := withFile(t, "film.mkv", data)
	mux := mediaMux(e.l)
	w := get(t, mux, mediaURL(file, "film.mkv"), fromPhone, "Range", "bytes=2-5")
	if w.Code != 206 || w.Body.String() != "2345" || w.Header().Get("Content-Type") != "video/x-matroska" {
		t.Errorf("Range: %d %q %s", w.Code, w.Body.String(), w.Header().Get("Content-Type"))
	}
	if w := get(t, mux, "/media/999/film.mkv", fromPhone); w.Code != 404 {
		t.Errorf("чужой номер: %d", w.Code)
	}
	if w := get(t, mux, "/media/abc/film.mkv", fromPhone); w.Code != 400 && w.Code != 404 {
		t.Errorf("мусор: %d", w.Code)
	}
	e.dl.set(malahit(5))
	e.scan(t)
	var tfile int64
	e.d.R.QueryRow(`SELECT id FROM lib_files WHERE tindex >= 0`).Scan(&tfile)
	if w := get(t, mux, mediaURL(tfile, "x.mkv"), fromPhone); w.Code != 404 {
		t.Errorf("файл раздачи через /media: %d", w.Code)
	}
}

// blockingWriter — ответ, который ждёт, пока тест не разрешит писать.
type blockingWriter struct {
	*httptest.ResponseRecorder
	once    sync.Once
	started chan struct{}
	go_     chan struct{}
}

func (b *blockingWriter) Write(p []byte) (int, error) {
	b.once.Do(func() { close(b.started) })
	<-b.go_
	return b.ResponseRecorder.Write(p)
}

// Пока идёт поток, ПК не засыпает.
func TestMediaKeepsAwake(t *testing.T) {
	e, file, _, _ := withFile(t, "film.mkv", []byte("0123456789"))
	keeper := power.New(slog.New(slog.DiscardHandler))
	t.Cleanup(func() { keeper.Close() })
	e.l.o.Power = keeper
	mux := mediaMux(e.l)
	r := httptest.NewRequest("GET", mediaURL(file, "film.mkv"), nil)
	r.RemoteAddr = fromPhone
	bw := &blockingWriter{ResponseRecorder: httptest.NewRecorder(), started: make(chan struct{}), go_: make(chan struct{})}
	done := make(chan struct{})
	go func() {
		mux.ServeHTTP(bw, r)
		close(done)
	}()
	<-bw.started
	if keeper.Active() != 1 {
		t.Errorf("во время потока запрет сна не держится: %d", keeper.Active())
	}
	close(bw.go_)
	<-done
	if keeper.Active() != 0 {
		t.Errorf("поток кончился, а поток всё ещё считается: %d", keeper.Active())
	}
}

func quick(t *testing.T) {
	t.Helper()
	was := [3]time.Duration{watch.Every, watch.Min, watch.Gap}
	wasLead, wasJump, wasExtra := watch.Lead, watch.JumpRead, mediaExtraLead
	watch.Every, watch.Min, watch.Gap, watch.Lead, watch.JumpRead, mediaExtraLead = 0, 0, 0, 0, 1, 0
	t.Cleanup(func() {
		watch.Every, watch.Min, watch.Gap = was[0], was[1], was[2]
		watch.Lead, watch.JumpRead, mediaExtraLead = wasLead, wasJump, wasExtra
	})
}

// Место по чтению файла уходит в историю устройства: «раздача» — lib-<единица>, номер — файл.
func TestMediaHistory(t *testing.T) {
	quick(t)
	e, file, unit, _ := withFile(t, "film.mkv", make([]byte, 1000))
	mux := mediaMux(e.l)
	get(t, mux, mediaURL(file, "film.mkv"), fromPhone, "Range", "bytes=0-499")
	e.clk.add(time.Second)
	e.l.tracker.Tick(e.clk.now())
	e.clk.add(time.Minute)
	e.l.tracker.Tick(e.clk.now())
	fs, _ := e.hist.Files(ctx, "192.168.0.50", "lib-"+strconv.FormatInt(unit, 10))
	if len(fs) != 1 || fs[0].Index != int(file) || fs[0].Fraction != 0.5 {
		t.Errorf("история: %+v", fs)
	}
}

// Review Focus 2: кириллица, пробелы, #, %, скобки в имени — ссылка экранирована, файл находится по
// номеру.
func TestMediaNameEscaping(t *testing.T) {
	name := "Холоп 3 [1080p] #1 100%.mkv"
	e, file, _, _ := withFile(t, name, []byte("data"))
	mux := mediaMux(e.l)
	w := get(t, mux, "/api/v1/library/files/"+strconv.FormatInt(file, 10)+"/play", fromPhone)
	var out struct {
		StreamURL string `json:"streamUrl"`
		M3UURL    string `json:"m3uUrl"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 200 {
		t.Fatalf("play: %d %s", w.Code, w.Body.String())
	}
	if strings.ContainsAny(out.StreamURL, " #%[") && !strings.Contains(out.StreamURL, "%25") {
		t.Errorf("не экранировано: %s", out.StreamURL)
	}
	path := strings.TrimPrefix(out.StreamURL, "http://127.0.0.1:8090")
	if w := get(t, mux, path, fromPhone); w.Code != 200 || w.Body.String() != "data" {
		t.Errorf("по экранированной ссылке: %d %q (%s)", w.Code, w.Body.String(), path)
	}
	w = get(t, mux, strings.TrimPrefix(out.M3UURL, "http://127.0.0.1:8090"), fromPhone)
	if w.Code != 200 || !strings.Contains(w.Body.String(), path) {
		t.Errorf(".m3u8: %d %s", w.Code, w.Body.String())
	}
}

// failingFile — файл, чтение которого обрывается (отключили диск).
type failingFile struct {
	*os.File
	left int
}

func (f *failingFile) Read(p []byte) (int, error) {
	if f.left <= 0 {
		return 0, errors.New("устройство не готово")
	}
	n, err := f.File.Read(p[:min(len(p), f.left)])
	f.left -= n
	return n, err
}

// Review Focus 4: файл пропал — 404, истории нет; чтение оборвалось — сервер жив, место не 100 %.
func TestMediaFileGone(t *testing.T) {
	quick(t)
	e, file, unit, p := withFile(t, "film.mkv", make([]byte, 1000))
	mux := mediaMux(e.l)
	e.l.open = func(name string) (mediaReader, error) {
		f, err := os.Open(name)
		if err != nil {
			return nil, err
		}
		return &failingFile{File: f, left: 300}, nil
	}
	get(t, mux, mediaURL(file, "film.mkv"), fromPhone)
	e.clk.add(time.Minute)
	e.l.tracker.Tick(e.clk.now())
	fs, _ := e.hist.Files(ctx, "192.168.0.50", "lib-"+strconv.FormatInt(unit, 10))
	if len(fs) != 1 || fs[0].Fraction >= 0.9 || fs[0].Watched {
		t.Errorf("после обрыва: %+v", fs)
	}
	e.l.open = nil
	os.Remove(p)
	if w := get(t, mux, mediaURL(file, "film.mkv"), fromPC); w.Code != 404 {
		t.Errorf("файла нет: %d", w.Code)
	}
	if fs, _ := e.hist.Files(ctx, "pc", "lib-"+strconv.FormatInt(unit, 10)); len(fs) != 0 {
		t.Errorf("история у пропавшего файла: %+v", fs)
	}
}

// .m3u8 — выбранная серия и следующие того же сезона; start-time — только у первой.
func TestM3USeries(t *testing.T) {
	e := newEnv(t)
	e.folder(t, catSeries, "Series", "Show/Show.S01E01.mkv", "Show/Show.S01E02.mkv", "Show/Show.S01E03.mkv", "Show/Show.S02E01.mkv")
	e.scan(t)
	var e2 int64
	e.d.R.QueryRow(`SELECT id FROM lib_files WHERE season = 1 AND episode = 2`).Scan(&e2)
	w := get(t, mediaMux(e.l), "/m3u/library/"+strconv.FormatInt(e2, 10)+".m3u8?start=90", fromPhone)
	body := w.Body.String()
	if w.Code != 200 || strings.Count(body, "http://") != 2 || !strings.Contains(body, "Show.S01E02.mkv") ||
		!strings.Contains(body, "Show.S01E03.mkv") || strings.Contains(body, "S02E01") || strings.Count(body, "start-time=90") != 1 ||
		strings.Index(body, "start-time") > strings.Index(body, "Show.S01E02.mkv") {
		t.Errorf(".m3u8 серий:\n%s", body)
	}
}

// «Смотреть»: место из истории устройства; «С начала» — с нуля; kinodom:// — только на этом ПК, на
// .m3u8 (серии подряд), место — в .m3u8, а не в ссылке (иначе VLC начал бы с него каждую серию).
func TestPlay(t *testing.T) {
	e, file, unit, _ := withFile(t, "film.mkv", make([]byte, 10))
	hash := "lib-" + strconv.FormatInt(unit, 10)
	e.hist.SetPosition(ctx, "pc", hash, int(file), 700, 5400)
	mux := mediaMux(e.l)
	play := func(from, q string) (out struct {
		StartSec  int     `json:"startSec"`
		M3UURL    string  `json:"m3uUrl"`
		LaunchURL *string `json:"launchUrl"`
		Title     string  `json:"title"`
	}) {
		w := get(t, mux, "/api/v1/library/files/"+strconv.FormatInt(file, 10)+"/play"+q, from)
		if w.Code != 200 {
			t.Fatalf("play: %d %s", w.Code, w.Body.String())
		}
		json.Unmarshal(w.Body.Bytes(), &out)
		return out
	}
	p := play(fromPC, "")
	if p.StartSec != 690 || !strings.HasSuffix(p.M3UURL, ".m3u8?start=690") || p.LaunchURL == nil ||
		!strings.Contains(*p.LaunchURL, "m3u%2Flibrary") || strings.Contains(*p.LaunchURL, "&start=") || p.Title == "" {
		t.Errorf("с места на ПК: %+v %v", p, *p.LaunchURL)
	}
	if p := play(fromPC, "?fromStart=1"); p.StartSec != 0 || strings.Contains(p.M3UURL, "start") {
		t.Errorf("с начала: %+v", p)
	}
	if p := play(fromPhone, ""); p.LaunchURL != nil || p.StartSec != 0 {
		t.Errorf("с телефона — своя история, без kinodom://: %+v", p)
	}
	if w := get(t, mux, "/api/v1/library/files/999/play", fromPhone); w.Code != 404 {
		t.Errorf("нет файла: %d", w.Code)
	}
	_ = io.EOF
}

// Файл с локального диска VLC читает впереди на весь буфер (вживую 2026-09-30: записано 86 с при
// картинке на ~40-й) — у /media поправка больше, чем у раздач.
func TestMediaLead(t *testing.T) {
	quick(t)
	mediaExtraLead = 100
	e, file, unit, _ := withFile(t, "film.mkv", make([]byte, 10000))
	get(t, mediaMux(e.l), mediaURL(file, "film.mkv"), fromPhone, "Range", "bytes=0-4099")
	e.clk.add(time.Minute)
	e.l.tracker.Tick(e.clk.now())
	fs, _ := e.hist.Files(ctx, "192.168.0.50", "lib-"+strconv.FormatInt(unit, 10))
	if len(fs) != 1 || fs[0].Fraction != 0.4 {
		t.Errorf("место с поправкой: %+v", fs)
	}
	mediaExtraLead = 1 << 20 // больше 2 % файла — поправка 2 %
	get(t, mediaMux(e.l), mediaURL(file, "film.mkv"), "192.168.0.51:5000", "Range", "bytes=0-4199")
	e.clk.add(time.Minute)
	e.l.tracker.Tick(e.clk.now())
	fs, _ = e.hist.Files(ctx, "192.168.0.51", "lib-"+strconv.FormatInt(unit, 10))
	if len(fs) != 1 || fs[0].Fraction != 0.4 {
		t.Errorf("поправка не больше 2 %%: %+v", fs)
	}
}
