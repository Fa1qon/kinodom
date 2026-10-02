package api

import (
	"compress/gzip"
	"mime"
	"net/http"
	"strings"
)

// Сжатие ответов (план 16А): список каналов — 242 КБ, по Wi-Fi телевизора это заметно. Текстовые ответы
// сжимаются gzip, если клиент его принимает; потоки, пересылка каналов, картинки и APK идут как есть — по пути, а
// не только по типу: обёртка не должна оказаться между плеером и видео.

// gzipTypes — что сжимается.
var gzipTypes = map[string]bool{
	"application/json": true, "text/html": true, "text/css": true, "text/javascript": true,
	"application/javascript": true, "image/svg+xml": true, "text/plain": true,
}

// gzipSkip — пути, которые не трогаются вовсе: потоки и пересылка (Flush, Range), файлы медиатеки, плейлисты
// плееров, картинки, APK.
var gzipSkip = []string{"/stream/", "/media/", "/m3u/", "/api/v1/iptv/relay", "/img/", "/logo/", "/app/"}

// gzipMin — меньше не сжимается: заголовки и сжатие дороже выигрыша.
const gzipMin = 1024

func gzipped(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(strings.ToLower(r.Header.Get("Accept-Encoding")), "gzip") || r.Header.Get("Range") != "" {
			next.ServeHTTP(w, r)
			return
		}
		for _, p := range gzipSkip {
			if strings.HasPrefix(r.URL.Path, p) {
				next.ServeHTTP(w, r)
				return
			}
		}
		g := &gzipWriter{w: w}
		defer g.finish()
		next.ServeHTTP(g, r)
	})
}

// gzipWriter копит начало ответа до gzipMin: короткий ответ уходит как есть, длинный текстовый 200 — сжатым.
type gzipWriter struct {
	w     http.ResponseWriter
	code  int
	buf   []byte
	zw    *gzip.Writer
	plain bool // решено: как есть
}

func (g *gzipWriter) Header() http.Header { return g.w.Header() }

func (g *gzipWriter) WriteHeader(code int) {
	if g.code != 0 {
		return
	}
	g.code = code
	if code != http.StatusOK || g.w.Header().Get("Content-Encoding") != "" || !compressible(g.w.Header().Get("Content-Type")) {
		g.passthrough()
	}
}

func (g *gzipWriter) Write(b []byte) (int, error) {
	if g.code == 0 {
		g.WriteHeader(http.StatusOK)
	}
	switch {
	case g.plain:
		return g.w.Write(b)
	case g.zw != nil:
		return g.zw.Write(b)
	}
	g.buf = append(g.buf, b...)
	if len(g.buf) >= gzipMin {
		if err := g.start(); err != nil {
			return 0, err
		}
	}
	return len(b), nil
}

func (g *gzipWriter) passthrough() {
	g.plain = true
	g.w.WriteHeader(g.code)
	if len(g.buf) > 0 {
		g.w.Write(g.buf)
		g.buf = nil
	}
}

func (g *gzipWriter) start() error {
	h := g.w.Header()
	h.Set("Content-Encoding", "gzip")
	h.Add("Vary", "Accept-Encoding")
	h.Del("Content-Length")
	g.w.WriteHeader(g.code)
	g.zw = gzip.NewWriter(g.w)
	_, err := g.zw.Write(g.buf)
	g.buf = nil
	return err
}

// Flush — ответ частями (пересылка берёт Flusher без проверки): решение принимается сейчас, накопленное уходит.
func (g *gzipWriter) Flush() {
	if g.code == 0 {
		g.WriteHeader(http.StatusOK)
	}
	if !g.plain && g.zw == nil {
		g.passthrough()
	}
	if g.zw != nil {
		g.zw.Flush()
	}
	if f, ok := g.w.(http.Flusher); ok {
		f.Flush()
	}
}

// finish — конец ответа: сжатое закрывается, короткое уходит как есть.
func (g *gzipWriter) finish() {
	switch {
	case g.zw != nil:
		g.zw.Close()
	case g.plain:
	case g.code != 0:
		g.passthrough()
	}
}

func compressible(contentType string) bool {
	t, _, err := mime.ParseMediaType(contentType)
	return err == nil && gzipTypes[t]
}
