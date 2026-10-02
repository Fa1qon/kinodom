package api

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// План 16А: список каналов — 242 КБ без сжатия; текстовые ответы сжимаются, если клиент принимает gzip.
func TestGzipJSON(t *testing.T) {
	big := `{"x":"` + strings.Repeat("канал ", 2000) + `"}`
	h := gzipped(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, big)
	}))
	req := httptest.NewRequest("GET", "/api/v1/channels", nil)
	req.Header.Set("Accept-Encoding", "gzip, deflate")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Header().Get("Content-Encoding") != "gzip" || !strings.Contains(rec.Header().Get("Vary"), "Accept-Encoding") {
		t.Fatalf("заголовки %v", rec.Header())
	}
	if rec.Body.Len() >= len(big)/4 {
		t.Fatalf("сжато до %d из %d", rec.Body.Len(), len(big))
	}
	zr, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := io.ReadAll(zr); string(b) != big {
		t.Fatal("тело не совпало")
	}
}

// Review Focus 4: без сжатия — клиент не принимает, ответ маленький, Range, 206, поток, пересылка, картинка,
// уже сжатый ответ.
func TestGzipSkips(t *testing.T) {
	text := strings.Repeat("a", 4000)
	cases := []struct {
		name, path, ctype, enc, rng, already string
		code                                 int
		body                                 string
	}{
		{"нет gzip у клиента", "/api/v1/x", "application/json", "", "", "", 200, text},
		{"маленький", "/api/v1/x", "application/json", "gzip", "", "", 200, "{}"},
		{"Range", "/app.js", "text/javascript", "gzip", "bytes=0-9", "", 200, text},
		{"206", "/api/v1/x", "text/plain", "gzip", "", "", 206, text},
		{"поток", "/stream/h/0/a.txt", "text/plain", "gzip", "", "", 200, text},
		{"пересылка", "/api/v1/iptv/relay?s=1&u=x&sig=y", "text/plain", "gzip", "", "", 200, text},
		{"файл медиатеки", "/media/1/a.srt", "text/plain", "gzip", "", "", 200, text},
		{"плейлист", "/m3u/library/a.m3u", "text/plain", "gzip", "", "", 200, text},
		{"картинка", "/img/x", "image/jpeg", "gzip", "", "", 200, text},
		{"не текст", "/app/kinodom.apk", "application/vnd.android.package-archive", "gzip", "", "", 200, text},
		{"уже сжат", "/api/v1/x", "application/json", "gzip", "", "br", 200, text},
	}
	for _, c := range cases {
		h := gzipped(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", c.ctype)
			if c.already != "" {
				w.Header().Set("Content-Encoding", c.already)
			}
			w.WriteHeader(c.code)
			io.WriteString(w, c.body)
		}))
		req := httptest.NewRequest("GET", c.path, nil)
		if c.enc != "" {
			req.Header.Set("Accept-Encoding", c.enc)
		}
		if c.rng != "" {
			req.Header.Set("Range", c.rng)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Header().Get("Content-Encoding") != c.already || rec.Body.String() != c.body || rec.Code != c.code {
			t.Errorf("%s: %q, код %d, тело %d", c.name, rec.Header().Get("Content-Encoding"), rec.Code, rec.Body.Len())
		}
	}
}

// Пересылка каналов (/api/v1/iptv/relay) и поток видео отдаются частями (Flush) — обёртка их не касается, а где
// касается — Flusher у неё есть (пересылка берёт его без проверки).
func TestGzipKeepsFlusherForStreams(t *testing.T) {
	for _, path := range []string{"/api/v1/iptv/relay?s=1", "/stream/h/0/a.mkv", "/media/1/a.mkv", "/api/v1/other"} {
		var ok bool
		h := gzipped(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, ok = w.(http.Flusher) }))
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Accept-Encoding", "gzip")
		h.ServeHTTP(httptest.NewRecorder(), req)
		if !ok {
			t.Errorf("%s: Flusher потерян", path)
		}
	}
}

// Файлы пульта — с ETag: повторный запуск приложения получает 304, а не весь пульт заново.
func TestPultETag(t *testing.T) {
	s, _ := newTestServerWeb(t, fstest.MapFS{"index.html": {Data: []byte("<h1>Kinodom</h1>")}, "app.js": {Data: []byte("export {}")}})
	rec := do(s.Handler(), httptest.NewRequest("GET", "/app.js", nil))
	tag := rec.Header().Get("ETag")
	if rec.Code != 200 || tag == "" || rec.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("код %d, ETag %q, Cache-Control %q", rec.Code, tag, rec.Header().Get("Cache-Control"))
	}
	req := httptest.NewRequest("GET", "/app.js", nil)
	req.Header.Set("If-None-Match", tag)
	if rec := do(s.Handler(), req); rec.Code != http.StatusNotModified {
		t.Fatalf("повтор: код %d", rec.Code)
	}
}

// Ревью 16А, Minor 6–7: сжатый ответ — без Accept-Ranges и со слабым ETag (тело другое); смотреть поток канала
// (/api/v1/iptv/streams/{id}/watch — та же пересылка) — по пути без сжатия.
func TestGzipHeadersAndWatchPath(t *testing.T) {
	text := strings.Repeat("a", 4000)
	h := gzipped(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css")
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("ETag", `"abc"`)
		io.WriteString(w, text)
	}))
	req := httptest.NewRequest("GET", "/style.css", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Header().Get("Content-Encoding") != "gzip" || rec.Header().Get("Accept-Ranges") != "" || rec.Header().Get("ETag") != `W/"abc"` {
		t.Fatalf("заголовки %v", rec.Header())
	}
	req = httptest.NewRequest("GET", "/api/v1/iptv/streams/5/watch", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Header().Get("Content-Encoding") != "" {
		t.Fatal("поток канала сжат")
	}
}
