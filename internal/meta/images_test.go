package meta

import (
	"bytes"
	"errors"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
)

// pngBytes — настоящая маленькая картинка PNG.
func pngBytes(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 2, 3))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// imageSite — хостинг картинок: /p.png — картинка, /page — HTML-заглушка, /big — 11 МБ,
// /kp/1.jpg — редирект на /images/no-poster.gif (как у Кинопоиска без постера).
func imageSite(t *testing.T, hits *atomic.Int32) *httptest.Server {
	t.Helper()
	pic := pngBytes(t)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		switch r.URL.Path {
		case "/p.png":
			w.Write(pic)
		case "/page":
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte("<html><body>Картинка удалена</body></html>"))
		case "/big":
			w.Write(append(pic, make([]byte, 11<<20)...))
		case "/kp/1.jpg":
			http.Redirect(w, r, "/images/no-poster.gif", http.StatusFound)
		case "/images/no-poster.gif":
			w.Write([]byte("GIF89a\x01\x00\x01\x00\x00\x00\x00;"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func newImages(t *testing.T, proxy string) *Images {
	t.Helper()
	im, err := NewImages(ImagesOptions{Dir: t.TempDir(), Proxy: proxy, Rate: 1000})
	if err != nil {
		t.Fatal(err)
	}
	return im
}

func TestFetchStoresImageAndServesIt(t *testing.T) {
	var hits atomic.Int32
	s := imageSite(t, &hits)
	im := newImages(t, "")
	key, err := im.Fetch(ctx, s.URL+"/p.png", Direct)
	if err != nil || key != ImageKey(s.URL+"/p.png") {
		t.Fatalf("ключ %q, %v", key, err)
	}
	if _, err := im.Fetch(ctx, s.URL+"/p.png", Direct); err != nil || hits.Load() != 1 {
		t.Fatalf("повторно скачано: заходов %d, %v", hits.Load(), err)
	}
	mux := http.NewServeMux()
	mux.Handle("GET /img/{key}", im.Handler())
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/img/"+key, nil))
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/png" || !bytes.Equal(rec.Body.Bytes(), pngBytes(t)) ||
		!strings.Contains(rec.Header().Get("Cache-Control"), "immutable") {
		t.Fatalf("ответ %d %q %q", rec.Code, rec.Header().Get("Content-Type"), rec.Header().Get("Cache-Control"))
	}
}

// Не картинка — ErrNoImage и ни одного файла: заглушка хостинга, «нет постера», не http(s).
func TestFetchRejectsNonImages(t *testing.T) {
	var hits atomic.Int32
	s := imageSite(t, &hits)
	im := newImages(t, "")
	for _, src := range []string{s.URL + "/page", s.URL + "/kp/1.jpg", "javascript:alert(1)", "data:image/png;base64,AAAA", "ftp://x/y.png"} {
		if _, err := im.Fetch(ctx, src, Direct); !errors.Is(err, ErrNoImage) {
			t.Errorf("%s: %v", src, err)
		}
	}
	if _, err := im.Fetch(ctx, s.URL+"/big", Direct); err == nil || errors.Is(err, ErrNoImage) {
		t.Errorf("большой файл: %v", err)
	}
	entries, _ := os.ReadDir(im.o.Dir)
	if len(entries) != 0 {
		t.Fatalf("в кэше остались файлы: %v", entries)
	}
}

// Картинки раздач — через прокси трекеров, Кинопоиск — напрямую.
func TestFetchViaProxy(t *testing.T) {
	pic := pngBytes(t)
	var proxied atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxied.Add(1) // HTTP-прокси получает полный адрес: отвечаем картинкой сами
		w.Write(pic)
	}))
	t.Cleanup(proxy.Close)
	var hits atomic.Int32
	s := imageSite(t, &hits)
	im := newImages(t, proxy.URL)
	if _, err := im.Fetch(ctx, s.URL+"/p.png", ViaProxy); err != nil || proxied.Load() != 1 || hits.Load() != 0 {
		t.Fatalf("через прокси: %v, прокси %d, сайт %d", err, proxied.Load(), hits.Load())
	}
	if _, err := im.Fetch(ctx, s.URL+"/p.png?direct", Direct); err != nil || proxied.Load() != 1 || hits.Load() != 1 {
		t.Fatalf("напрямую: %v, прокси %d, сайт %d", err, proxied.Load(), hits.Load())
	}
}

// Ключ /img — только 40 шестнадцатеричных знаков: «..» и прочее — 404.
func TestHandlerRejectsBadKeys(t *testing.T) {
	im := newImages(t, "")
	os.WriteFile(im.o.Dir+"/../secret.png", pngBytes(t), 0o644)
	mux := http.NewServeMux()
	mux.Handle("GET /img/{key}", im.Handler())
	for _, key := range []string{"..%2Fsecret", "zz", strings.Repeat("a", 40)} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", "/img/"+key, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: код %d", key, rec.Code)
		}
	}
}
