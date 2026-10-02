package iptv

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// План 14Д: каждая ссылка списка HLS (сегменты, вложенные списки, URI ключей и карт) — на пересылку, от
// адреса списка; остальные строки — как были (Review Focus 2).
func TestRewritePlaylist(t *testing.T) {
	base, _ := url.Parse("https://cdn.example/live/ch1/index.m3u8?token=1")
	in := "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-KEY:METHOD=AES-128,URI=\"key.bin\",IV=0x1\n#EXT-X-MAP:URI=\"init.mp4\"\n" +
		"#EXTINF:6.0,\nseg/1.ts\n#EXTINF:6.0,\n/abs/2.ts\n\n#EXT-X-STREAM-INF:BANDWIDTH=800000\n../hd/index.m3u8\nhttps://other.example/3.ts\r\n"
	link := func(abs string) string { return "R(" + abs + ")" }
	got := string(rewritePlaylist([]byte(in), base, link))
	for _, want := range []string{
		"#EXT-X-VERSION:3\n",
		`#EXT-X-KEY:METHOD=AES-128,URI="R(https://cdn.example/live/ch1/key.bin)",IV=0x1`,
		`#EXT-X-MAP:URI="R(https://cdn.example/live/ch1/init.mp4)"`,
		"\nR(https://cdn.example/live/ch1/seg/1.ts)\n",
		"\nR(https://cdn.example/abs/2.ts)\n",
		"\nR(https://cdn.example/live/hd/index.m3u8)\n",
		"\nR(https://other.example/3.ts)",
		"#EXTINF:6.0,\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("нет %q в:\n%s", want, got)
		}
	}
}

// relaySrc — источник каналов для пересылки: проверяет заголовки записи, считает запросы.
type relaySrc struct {
	srv  *httptest.Server
	hits atomic.Int32
	mu   sync.Mutex
	ua   map[string]string // путь → User-Agent последнего запроса (фоновая проверка ходит к тем же адресам)
	ref  map[string]string
	gone chan struct{} // поток live: клиент ушёл
}

func newRelaySrc(t *testing.T) *relaySrc {
	t.Helper()
	s := &relaySrc{gone: make(chan struct{}, 1), ua: map[string]string{}, ref: map[string]string{}}
	mux := http.NewServeMux()
	note := func(r *http.Request) {
		s.hits.Add(1)
		s.mu.Lock()
		s.ua[r.URL.Path], s.ref[r.URL.Path] = r.UserAgent(), r.Referer()
		s.mu.Unlock()
	}
	mux.HandleFunc("/hls/master.m3u8", func(w http.ResponseWriter, r *http.Request) {
		note(r)
		http.Redirect(w, r, "/cdn/a/master.m3u8", http.StatusFound)
	})
	mux.HandleFunc("/cdn/a/master.m3u8", func(w http.ResponseWriter, r *http.Request) {
		note(r)
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		io.WriteString(w, "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\nhd/index.m3u8\n")
	})
	mux.HandleFunc("/cdn/a/hd/index.m3u8", func(w http.ResponseWriter, r *http.Request) {
		note(r)
		io.WriteString(w, "#EXTM3U\n#EXTINF:6,\nseg1.ts\n")
	})
	mux.HandleFunc("/cdn/a/hd/seg1.ts", func(w http.ResponseWriter, r *http.Request) {
		note(r)
		w.Header().Set("Content-Type", "video/mp2t")
		w.Write([]byte("TS-DATA"))
	})
	mux.HandleFunc("/live.ts", func(w http.ResponseWriter, r *http.Request) {
		note(r)
		w.Header().Set("Content-Type", "video/mp2t")
		for {
			if _, err := w.Write(make([]byte, 188*20)); err != nil {
				break
			}
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
				s.gone <- struct{}{}
				return
			case <-time.After(10 * time.Millisecond):
			}
		}
		s.gone <- struct{}{}
	})
	mux.HandleFunc("/dash.mpd", func(w http.ResponseWriter, r *http.Request) { note(r); io.WriteString(w, "<MPD/>") })
	s.srv = httptest.NewServer(mux)
	t.Cleanup(s.srv.Close)
	return s
}

// relayModule — модуль с плейлистом источников src и маршрутами на ServeMux; номера источников по адресам.
func relayModule(t *testing.T, src *relaySrc) (*Module, *http.ServeMux, map[string]int64) {
	t.Helper()
	f := newFakeNet(t)
	m, _ := startModuleWith(t, f, quietProber())
	u := src.srv.URL
	f.setPlaylist("#EXTM3U\n#EXTINF:-1,Канал HLS\n#EXTVLCOPT:http-user-agent=TestUA/1\n#EXTVLCOPT:http-referrer=https://ref.example/\n" + u + "/hls/master.m3u8\n" +
		"#EXTINF:-1,Канал TS\n" + u + "/live.ts\n#EXTINF:-1,Канал DASH\n" + u + "/dash.mpd\n")
	if _, err := m.AddPlaylist(context.Background(), PlaylistInput{URL: f.srv.URL + "/pl.m3u"}); err != nil {
		t.Fatal(err)
	}
	ids := map[string]int64{}
	m.mu.Lock()
	for _, p := range []string{"/hls/master.m3u8", "/live.ts", "/dash.mpd"} {
		if s := m.pool.byURL[u+p]; s != nil {
			ids[p] = s.ID
		}
	}
	m.mu.Unlock()
	if len(ids) != 3 {
		t.Fatalf("источники: %v", ids)
	}
	mux := http.NewServeMux()
	m.Register(testRouter{mux}, nil)
	return m, mux, ids
}

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	req.RemoteAddr = fromPhone
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// relayLinks — ссылки на пересылку из списка.
func relayLinks(body string) []string {
	var out []string
	for _, l := range strings.Split(body, "\n") {
		if strings.HasPrefix(l, "/api/v1/iptv/relay?") {
			out = append(out, l)
		}
	}
	return out
}

// HLS через пересылку: список — со ссылками на пересылку, вложенный список переписан снова, сегмент — байты
// источника; заголовки — из записи плейлиста; после переадресации — от нового адреса.
func TestRelayHLS(t *testing.T) {
	src := newRelaySrc(t)
	_, mux, ids := relayModule(t, src)
	rec := get(t, mux, fmt.Sprintf("/api/v1/iptv/streams/%d/watch", ids["/hls/master.m3u8"]))
	if rec.Code != 200 || !strings.Contains(rec.Header().Get("Content-Type"), "mpegurl") {
		t.Fatalf("список: %d %s", rec.Code, rec.Body)
	}
	links := relayLinks(rec.Body.String())
	if len(links) != 1 || !strings.Contains(links[0], url.QueryEscape(src.srv.URL+"/cdn/a/hd/index.m3u8")) {
		t.Fatalf("вложенный список: %v", links)
	}
	rec = get(t, mux, links[0])
	seg := relayLinks(rec.Body.String())
	if rec.Code != 200 || len(seg) != 1 {
		t.Fatalf("вложенный список через пересылку: %d %s", rec.Code, rec.Body)
	}
	rec = get(t, mux, seg[0])
	if rec.Code != 200 || rec.Body.String() != "TS-DATA" {
		t.Fatalf("сегмент: %d %q", rec.Code, rec.Body)
	}
	src.mu.Lock()
	ua, ref := src.ua["/cdn/a/hd/seg1.ts"], src.ref["/cdn/a/hd/seg1.ts"]
	src.mu.Unlock()
	if ua != "TestUA/1" || ref != "https://ref.example/" {
		t.Fatalf("заголовки сегмента: %q %q", ua, ref)
	}
}

// Подделанная ссылка — 403, к источнику не ходим (Review Focus 1).
func TestRelaySignature(t *testing.T) {
	src := newRelaySrc(t)
	m, mux, ids := relayModule(t, src)
	id := ids["/hls/master.m3u8"]
	good := m.relayLink(id, src.srv.URL+"/cdn/a/hd/seg1.ts")
	before := src.hits.Load()
	for name, path := range map[string]string{
		"чужой адрес":     strings.Replace(good, url.QueryEscape("/cdn/a/hd/seg1.ts"), url.QueryEscape("/live.ts"), 1),
		"другой источник": strings.Replace(good, fmt.Sprintf("s=%d", id), fmt.Sprintf("s=%d", ids["/live.ts"]), 1),
		"без подписи":     fmt.Sprintf("/api/v1/iptv/relay?s=%d&u=%s", id, url.QueryEscape("http://169.254.169.254/")),
	} {
		if rec := get(t, mux, path); rec.Code != http.StatusForbidden {
			t.Errorf("%s: %d", name, rec.Code)
		}
	}
	if n := src.hits.Load() - before; n != 0 {
		t.Fatalf("запросов к источнику: %d", n)
	}
	if rec := get(t, mux, good); rec.Code != 200 {
		t.Fatalf("настоящая ссылка: %d", rec.Code)
	}
}

// Поток MPEG-TS — как есть; клиент ушёл — пересылка обрывает источник (Review Focus 3).
func TestRelayTSStopsOnClose(t *testing.T) {
	src := newRelaySrc(t)
	_, mux, ids := relayModule(t, src)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.RemoteAddr = fromPhone
		mux.ServeHTTP(w, r)
	}))
	defer srv.Close()
	resp, err := http.Get(fmt.Sprintf("%s/api/v1/iptv/streams/%d/watch", srv.URL, ids["/live.ts"]))
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 188*40)
	if _, err := io.ReadFull(resp.Body, buf); err != nil || resp.Header.Get("Content-Type") != "video/mp2t" {
		t.Fatalf("поток: %v %q", err, resp.Header.Get("Content-Type"))
	}
	resp.Body.Close()
	select {
	case <-src.gone:
	case <-time.After(5 * time.Second):
		t.Fatal("источник продолжает отдавать после ухода клиента")
	}
}

// DASH — 409 («Открыть в VLC»); неизвестный источник — 404.
func TestRelayDASH(t *testing.T) {
	src := newRelaySrc(t)
	_, mux, ids := relayModule(t, src)
	if rec := get(t, mux, fmt.Sprintf("/api/v1/iptv/streams/%d/watch", ids["/dash.mpd"])); rec.Code != http.StatusConflict {
		t.Fatalf("DASH: %d", rec.Code)
	}
	if rec := get(t, mux, "/api/v1/iptv/streams/999999/watch"); rec.Code != http.StatusNotFound {
		t.Fatalf("неизвестный: %d", rec.Code)
	}
}

// relayOne — модуль с одним источником: адрес /x отдаёт handler (свой сервер — фоновая проверка других тестов
// сюда не ходит).
func relayOne(t *testing.T, handler http.HandlerFunc) (*http.ServeMux, int64) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	f := newFakeNet(t)
	m, _ := startModuleWith(t, f, quietProber())
	f.setPlaylist("#EXTM3U\n#EXTINF:-1,Канал\n" + srv.URL + "/x\n")
	if _, err := m.AddPlaylist(context.Background(), PlaylistInput{URL: f.srv.URL + "/pl.m3u"}); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	s := m.pool.byURL[srv.URL+"/x"]
	m.mu.Unlock()
	if s == nil {
		t.Fatal("источника нет")
	}
	mux := http.NewServeMux()
	m.Register(testRouter{mux}, nil)
	return mux, s.ID
}

// Ревью 14Д, п. 2: страница вместо потока не отдаётся страницей — иначе её скрипт работал бы от имени пульта;
// запрос с чужого сайта — 403.
func TestRelayNotAPage(t *testing.T) {
	page := "<html><script>fetch('/api/v1/torrents',{method:'POST'})</script></html>"
	for name, h := range map[string]http.HandlerFunc{
		"text/html": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			io.WriteString(w, page)
		},
		"без типа": func(w http.ResponseWriter, r *http.Request) {
			w.Header()["Content-Type"] = nil
			io.WriteString(w, page)
		},
	} {
		mux, id := relayOne(t, h)
		path := fmt.Sprintf("/api/v1/iptv/streams/%d/watch", id)
		rec := get(t, mux, path)
		if ct := rec.Header().Get("Content-Type"); rec.Code != 200 || ct != "application/octet-stream" ||
			rec.Header().Get("X-Content-Type-Options") != "nosniff" || !strings.Contains(rec.Header().Get("Content-Security-Policy"), "sandbox") {
			t.Errorf("%s: %d %q %v", name, rec.Code, ct, rec.Header())
		}
		req := httptest.NewRequest("GET", path, nil)
		req.RemoteAddr = fromPhone
		req.Header.Set("Sec-Fetch-Site", "cross-site")
		rec = httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s, с чужого сайта: %d", name, rec.Code)
		}
	}
}

// План 14Д, задача 1 (ревью, п. 6): источник не прислал ответ за relayWait — 504 словами, а не вечное ожидание.
func TestRelayHeaderTimeout(t *testing.T) {
	old := relayWait
	relayWait = 300 * time.Millisecond
	defer func() { relayWait = old }()
	mux, id := relayOne(t, func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	start := time.Now()
	rec := get(t, mux, fmt.Sprintf("/api/v1/iptv/streams/%d/watch", id))
	if rec.Code != http.StatusGatewayTimeout || !strings.Contains(rec.Body.String(), "не ответил") || time.Since(start) > 5*time.Second {
		t.Fatalf("%d %s за %v", rec.Code, rec.Body, time.Since(start))
	}
}

// Ревью 14Д, п. 8: ключи не http(s) (FairPlay skd://, встроенные data:) — как были, без пересылки; BOM снят.
func TestRewritePlaylistKeepsNonWeb(t *testing.T) {
	base, _ := url.Parse("https://cdn.example/live/index.m3u8")
	in := "\ufeff#EXTM3U\r\n#EXT-X-KEY:METHOD=SAMPLE-AES,URI=\"skd://key42\"\r\n#EXT-X-SESSION-KEY:METHOD=AES-128,URI=\"data:text/plain;base64,AAAA\"\r\n#EXTINF:6,\r\nseg.ts\r\n"
	got := string(rewritePlaylist([]byte(in), base, func(abs string) string { return "R(" + abs + ")" }))
	for _, want := range []string{"#EXTM3U\r\n", `URI="skd://key42"`, `URI="data:text/plain;base64,AAAA"`, "\nR(https://cdn.example/live/seg.ts)"} {
		if !strings.Contains(got, want) {
			t.Errorf("нет %q в:\n%s", want, got)
		}
	}
	if strings.HasPrefix(got, "\ufeff") || strings.Contains(got, "R(\ufeff") {
		t.Errorf("BOM остался:\n%q", got)
	}
}

// Ревью 14Д, п. 7: список с BOM и CRLF без типа mpegurl — тот же список: распознан, строки-теги не ссылки.
func TestRelayPlaylistWithBOM(t *testing.T) {
	mux, id := relayOne(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, "\ufeff#EXTM3U\r\n#EXTINF:6,\r\nseg1.ts\r\n")
	})
	rec := get(t, mux, fmt.Sprintf("/api/v1/iptv/streams/%d/watch", id))
	body := rec.Body.String()
	if !strings.Contains(rec.Header().Get("Content-Type"), "mpegurl") || !strings.HasPrefix(body, "#EXTM3U") || len(relayLinks(body)) != 1 {
		t.Fatalf("%q %q", rec.Header().Get("Content-Type"), body)
	}
}

// Ревью 14Д, п. 9: Range уходит источнику, ответ — 206 с Content-Range (#EXT-X-BYTERANGE, перемотка).
func TestRelayRange(t *testing.T) {
	data := bytes.Repeat([]byte("0123456789"), 100)
	mux, id := relayOne(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp2t")
		http.ServeContent(w, r, "x.ts", time.Time{}, bytes.NewReader(data))
	})
	rec := getRange(t, mux, fmt.Sprintf("/api/v1/iptv/streams/%d/watch", id), "bytes=10-19")
	if rec.Code != http.StatusPartialContent || rec.Body.String() != "0123456789" || rec.Header().Get("Content-Range") != "bytes 10-19/1000" {
		t.Fatalf("%d %q %v", rec.Code, rec.Body, rec.Header())
	}
}

// Review Focus 1 (15Г): источник не умеет Range (200 и весь файл) — пересылка отдаёт 200 и всё тело.
func TestRelayRangeIgnored(t *testing.T) {
	data := bytes.Repeat([]byte("ab"), 50)
	mux, id := relayOne(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp2t")
		w.Write(data)
	})
	rec := getRange(t, mux, fmt.Sprintf("/api/v1/iptv/streams/%d/watch", id), "bytes=10-19")
	if rec.Code != 200 || rec.Body.Len() != len(data) || rec.Header().Get("Content-Range") != "" {
		t.Fatalf("%d %d %v", rec.Code, rec.Body.Len(), rec.Header())
	}
}

func getRange(t *testing.T, h http.Handler, path, rg string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	req.RemoteAddr = fromPhone
	req.Header.Set("Range", rg)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}
