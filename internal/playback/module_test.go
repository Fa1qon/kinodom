package playback

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type testRouter struct{ mux *http.ServeMux }

func (r testRouter) Handle(p, _ string, h http.Handler) { r.mux.Handle(p, h) }

// fakeRes — модуль-источник: файл library/7 — testdata/sample.mkv (раздаётся по /files/), следующая серия — 8.
type fakeRes struct {
	err  error
	path string
}

func (f fakeRes) Resolve(r *http.Request, ref Ref, prepare, fromStart bool) (Source, error) {
	if f.err != nil {
		return Source{}, f.err
	}
	if ref.Kind != "library" || ref.File != 7 {
		return Source{}, &StatusError{Code: 404, Text: "такого файла в медиатеке нет"}
	}
	start := 3
	if fromStart {
		start = 0
	}
	p := f.path
	if p == "" {
		p = "/files/sample.mkv"
	}
	return Source{Title: "Пример — 1×01", Hash: "lib-1", Index: 7, Path: p, M3U: "/m3u/library/7.m3u8", StartSec: start,
		SubFiles: []string{filepath.Join("testdata", "sample.rus.srt")}, Next: &Ref{Kind: "library", File: 8}, NextTitle: "Пример — 1×02"}, nil
}

// fixture — модуль плеера на httptest: файлы testdata — по /files/ того же сервера (как поток на этом ПК).
func fixture(t *testing.T, res Resolver, ok bool) (*Module, *httptest.Server) {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle("GET /files/", http.StripPrefix("/files/", http.FileServer(http.Dir("testdata"))))
	mux.HandleFunc("GET /stall/", func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	mux.HandleFunc("GET /holey/sample.mkv", func(w http.ResponseWriter, r *http.Request) {
		data, _ := os.ReadFile(filepath.Join("testdata", "sample.mkv"))
		http.ServeContent(w, r, "", time.Time{}, &holey{data: data, from: int64(len(data)) * 6 / 10, to: int64(len(data)) * 9 / 10})
	})
	mux.HandleFunc("GET /junk/x.mkv", func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "", time.Time{}, strings.NewReader(strings.Repeat("не видео ", 5000)))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	var tl Tools
	if ok {
		tl = tools(t)
	}
	m := New(Options{Tools: tl, OK: ok, Res: res, Origin: func() string { return srv.URL }})
	m.Register(testRouter{mux})
	return m, srv
}

func getBody(t *testing.T, url string) (int, string, string) {
	t.Helper()
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header.Get("Content-Type"), string(b)
}

func TestInfo(t *testing.T) {
	_, srv := fixture(t, fakeRes{}, true)
	code, _, body := getBody(t, srv.URL+"/api/v1/play/library/7")
	var out struct {
		Src, Title, Hash, Direct, M3UURL string
		Index, StartSec                  int
		DurationSec                      float64
		Video                            Video
		Audio                            []Track
		Subs                             []Sub
		Next                             *struct{ Src, Title string }
	}
	json.Unmarshal([]byte(body), &out)
	host := strings.TrimPrefix(srv.URL, "http://")
	if code != 200 || out.Src != "library/7" || out.Title != "Пример — 1×01" || out.Hash != "lib-1" || out.Index != 7 || out.StartSec != 3 ||
		out.DurationSec < 11.9 || out.Direct != "http://"+host+"/files/sample.mkv?own=1" || out.M3UURL != "http://"+host+"/m3u/library/7.m3u8" ||
		out.Video.Mime != "avc1.640028" || len(out.Audio) != 2 || out.Next == nil || out.Next.Src != "library/8" || out.Next.Title != "Пример — 1×02" {
		t.Fatalf("сведения: %d %s", code, body)
	}
	if len(out.Subs) != 2 || out.Subs[0].ID != "3" || out.Subs[1].ID != "f0" || out.Subs[1].Lang != "rus" || out.Subs[1].Title != "sample.rus.srt" {
		t.Errorf("субтитры: %+v", out.Subs)
	}
	if _, _, body := getBody(t, srv.URL+"/api/v1/play/library/7?fromStart=1"); !strings.Contains(body, `"startSec":0`) {
		t.Errorf("с начала: %s", body)
	}
	if code, _, _ := getBody(t, srv.URL+"/api/v1/play/library/9"); code != 404 {
		t.Errorf("нет файла: %d", code)
	}
	if code, _, _ := getBody(t, srv.URL+"/api/v1/play/library/abc"); code != 400 {
		t.Errorf("номер не число: %d", code)
	}
}

func TestInfoErrors(t *testing.T) {
	_, srv := fixture(t, fakeRes{}, false)
	if code, _, body := getBody(t, srv.URL+"/api/v1/play/library/7"); code != 503 || !strings.Contains(body, "Плеер в браузере недоступен") {
		t.Errorf("без ffmpeg: %d %s", code, body)
	}
	_, srv = fixture(t, fakeRes{err: &StatusError{Code: 410, Text: "Файла больше нет"}}, true)
	if code, _, body := getBody(t, srv.URL+"/api/v1/play/library/7"); code != 410 || !strings.Contains(body, "Файла больше нет") {
		t.Errorf("ошибка модуля: %d %s", code, body)
	}
}

// Раздача ещё качается, заголовок не пришёл — ответ за ProbeTimeout, а не зависший запрос (Review Focus 1).
func TestInfoProbeTimeout(t *testing.T) {
	was := ProbeTimeout
	ProbeTimeout = 500 * time.Millisecond
	t.Cleanup(func() { ProbeTimeout = was })
	_, srv := fixture(t, fakeRes{path: "/stall/x.mkv"}, true)
	start := time.Now()
	code, _, body := getBody(t, srv.URL+"/api/v1/play/library/7")
	if code != 504 || !strings.Contains(body, "раздача ещё качается") || time.Since(start) > 5*time.Second {
		t.Errorf("зависший файл: %d %s за %v", code, body, time.Since(start))
	}
}

func TestKeyframeRoute(t *testing.T) {
	_, srv := fixture(t, fakeRes{}, true)
	if code, _, body := getBody(t, srv.URL+"/api/v1/play/library/7/keyframe?t=5.3"); code != 200 || strings.TrimSpace(body) != `{"t":4}` {
		t.Errorf("кадр: %d %s", code, body)
	}
	for _, q := range []string{"t=-1", "t=abc", ""} {
		if code, _, _ := getBody(t, srv.URL+"/api/v1/play/library/7/keyframe?"+q); code != 400 {
			t.Errorf("%q: %d", q, code)
		}
	}
}

func TestStreamRoute(t *testing.T) {
	m, srv := fixture(t, fakeRes{}, true)
	code, ct, body := getBody(t, srv.URL+"/play/library/7/stream.ts?t=4&a=1&sid=p1")
	if code != 200 || ct != "video/mp2t" || len(body) < 10000 || body[0] != 0x47 {
		t.Fatalf("TS: %d %s %d", code, ct, len(body))
	}
	code, ct, body = getBody(t, srv.URL+"/play/library/7/stream.mkv?t=4&a=1&s=f0&sid=p1")
	if code != 200 || ct != "video/x-matroska" || len(body) < 10000 {
		t.Fatalf("MKV: %d %s %d", code, ct, len(body))
	}
	for _, q := range []string{"t=4&a=9&sid=p1", "t=4&a=1", "t=4&a=1&sid=" + strings.Repeat("x", 65), "t=x&a=1&sid=p1"} {
		if code, _, _ := getBody(t, srv.URL+"/play/library/7/stream.ts?"+q); code != 400 {
			t.Errorf("%q: %d", q, code)
		}
	}
	if code, _, _ := getBody(t, srv.URL+"/play/library/7/stream.mkv?t=0&a=1&s=9&sid=p1"); code != 400 {
		t.Errorf("нет таких субтитров: %d", code)
	}
	if code, _, body := getBody(t, srv.URL+"/play/library/7/stream.ts?sid=p1"); code != 200 || len(body) < 10000 {
		t.Errorf("без a и t — главная озвучка с начала: %d %d", code, len(body))
	}
	waitActive(t, m, 0)
}

func waitActive(t *testing.T, m *Module, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for m.runs.Active() != n {
		if time.Now().After(deadline) {
			t.Fatalf("плееров %d, ждали %d", m.runs.Active(), n)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// open — поток плеера sid, прочитан первый кусок; закрыть — cancel.
func open(t *testing.T, url string) (*http.Response, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if resp.StatusCode == 200 {
		io.ReadFull(resp.Body, make([]byte, 1000))
	}
	return resp, cancel
}

// Четвёртый плеер — 429; тот же плеер (перемотка) — без нового места; закрыли — процесса нет (Review Focus 2).
func TestStreamBusyAndDisconnect(t *testing.T) {
	was := Burst
	Burst = 0 // в темпе просмотра: 12 с файла — поток не кончается сам
	t.Cleanup(func() { Burst = was })
	m, srv := fixture(t, fakeRes{}, true)
	var cancels []context.CancelFunc
	for _, sid := range []string{"a", "b", "c"} {
		resp, cancel := open(t, srv.URL+"/play/library/7/stream.ts?sid="+sid)
		if resp.StatusCode != 200 {
			t.Fatalf("%s: %d", sid, resp.StatusCode)
		}
		cancels = append(cancels, cancel)
	}
	resp, cancel := open(t, srv.URL+"/play/library/7/stream.ts?sid=d")
	b, _ := io.ReadAll(resp.Body)
	cancel()
	if resp.StatusCode != 429 || !strings.Contains(string(b), "трёх устройствах") {
		t.Fatalf("четвёртый: %d %s", resp.StatusCode, b)
	}
	resp, cancel = open(t, srv.URL+"/play/library/7/stream.ts?t=4&sid=a") // перемотка плеера a
	if resp.StatusCode != 200 || m.runs.Active() != 3 {
		t.Fatalf("перемотка: %d, плееров %d", resp.StatusCode, m.runs.Active())
	}
	cancels = append(cancels, cancel)
	for _, c := range cancels {
		c()
	}
	waitActive(t, m, 0)
}

func TestSubsRoute(t *testing.T) {
	_, srv := fixture(t, fakeRes{}, true)
	if code, ct, body := getBody(t, srv.URL+"/play/library/7/subs/3.vtt?t=4&sid=p1"); code != 200 || !strings.HasPrefix(ct, "text/vtt") || !strings.Contains(body, "Вторая реплика") {
		t.Errorf("встроенные: %d %s %s", code, ct, body)
	}
	if code, _, body := getBody(t, srv.URL+"/play/library/7/subs/f0.vtt?t=4&sid=p1"); code != 200 || !strings.Contains(body, "Внешняя два") {
		t.Errorf("внешние: %d %s", code, body)
	}
	for _, p := range []string{"9.vtt", "f5.vtt", "3.srt", "1.vtt"} {
		if code, _, _ := getBody(t, srv.URL+"/play/library/7/subs/"+p+"?t=0&sid=p1"); code != 404 {
			t.Errorf("%s: %d", p, code)
		}
	}
}

func TestExternalSubs(t *testing.T) {
	got := externalSubs([]string{`D:\S\Show.S01E01.rus.srt`, `D:\S\Subs\Show.S01E01.English.ass`, `D:\S\Show.S01E01.srt`})
	if len(got) != 3 || got[0] != (Sub{ID: "f0", Lang: "rus", Title: "Show.S01E01.rus.srt", File: `D:\S\Show.S01E01.rus.srt`}) ||
		got[1].Lang != "eng" || got[1].ID != "f1" || got[2].Lang != "" {
		t.Errorf("%+v", got)
	}
	_ = os.Remove
}

// holey — файл с «дырой» [from, to): чтение оттуда — ошибка (недокачанные куски раздачи, пропавший диск).
type holey struct {
	data     []byte
	from, to int64
	pos      int64
}

func (h *holey) Read(p []byte) (int, error) {
	if h.pos >= int64(len(h.data)) {
		return 0, io.EOF
	}
	if h.pos >= h.from && h.pos < h.to {
		return 0, errors.New("кусок не скачан")
	}
	end := int64(len(h.data))
	if h.pos < h.from {
		end = min(end, h.from)
	}
	n := copy(p, h.data[h.pos:end])
	h.pos += int64(n)
	return n, nil
}

func (h *holey) Seek(off int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		h.pos = off
	case io.SeekCurrent:
		h.pos += off
	case io.SeekEnd:
		h.pos = int64(len(h.data)) + off
	}
	return h.pos, nil
}

// Файл оборвался посреди (ревью 18А, Important 2): ffmpeg выходит с 0 и «Error during demuxing», а ответ должен
// оборваться — иначе плеер примет обрыв за конец фильма («просмотрено», следующая серия).
func TestStreamBrokenUpstream(t *testing.T) {
	_, srv := fixture(t, fakeRes{path: "/holey/sample.mkv"}, true)
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Get(srv.URL + "/play/library/7/stream.ts?sid=p1")
	if err != nil {
		t.Fatal(err)
	}
	b, rerr := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode == 200 && rerr == nil {
		t.Fatalf("оборванный файл — ответ кончился штатно (%d байт): плеер решит, что фильм досмотрен", len(b))
	}
}

// ffmpeg не смог начать (звука нет в сборке) — понятная ошибка, а не 200 с пустым телом (ревью 18А, Important 2).
func TestStreamFailsBeforeOutput(t *testing.T) {
	_, srv := fixture(t, fakeRes{path: "/files/adpcm.avi"}, true)
	code, _, body := getBody(t, srv.URL+"/play/library/7/stream.ts?sid=p1")
	if code == 200 || !strings.Contains(body, "Поток не запустился") || strings.Contains(body, "127.0.0.1") || strings.Contains(body, "ffmpeg") {
		t.Errorf("ffmpeg не начал: %d %s", code, body)
	}
}

// Тексты ошибок — без внутренних адресов и вывода ffmpeg (правило «без адресов», ревью 18А, Minor 4).
func TestInfoErrorHidesInternals(t *testing.T) {
	_, srv := fixture(t, fakeRes{path: "/junk/x.mkv"}, true)
	code, _, body := getBody(t, srv.URL+"/api/v1/play/library/7")
	if code != 422 || !strings.Contains(body, "Файл не читается") {
		t.Fatalf("мусор вместо видео: %d %s", code, body)
	}
	for _, bad := range []string{"127.0.0.1", "http", "ffprobe", "exit status", "own=1"} {
		if strings.Contains(body, bad) {
			t.Errorf("в тексте ошибки — %q: %s", bad, body)
		}
	}
}
