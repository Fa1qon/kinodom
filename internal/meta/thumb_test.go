package meta

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// План 16А: сетки пульта берут постеры шириной 400 px — на телевизоре большие постеры (до 425 КБ) разжимались
// секундами (замер 2026-10-02). Миниатюра — JPEG с пропорцией оригинала, лежит в кэше рядом с оригиналом.
func TestThumbnailJPEG(t *testing.T) {
	im := newImages(t, "")
	key := strings.Repeat("a", 40)
	writeJPEG(t, filepath.Join(im.o.Dir, key+".jpg"), 1000, 1500)
	rec := getImg(t, im, "/img/"+key+"?w=400")
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/jpeg" {
		t.Fatalf("код %d, тип %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	if w, h := imageSize(t, rec.Body.Bytes()); w != 400 || h != 600 {
		t.Fatalf("размер %d×%d", w, h)
	}
	if _, err := os.Stat(filepath.Join(im.o.Dir, key+".w400.jpg")); err != nil {
		t.Fatalf("миниатюра не легла в кэш: %v", err)
	}
	if !strings.Contains(rec.Header().Get("Cache-Control"), "immutable") {
		t.Fatalf("Cache-Control %q", rec.Header().Get("Cache-Control"))
	}
}

// WebP (большая часть постеров), PNG и GIF уменьшаются так же; другая ширина — та же одна (400).
func TestThumbnailFormats(t *testing.T) {
	im := newImages(t, "")
	webp, err := os.ReadFile("testdata/thumb-src.webp")
	if err != nil {
		t.Fatal(err)
	}
	srcs := map[string][]byte{"b": webp, "c": encodePNG(t, 800, 1200), "d": encodeGIF(t, 800, 1200)}
	exts := map[string]string{"b": ".webp", "c": ".png", "d": ".gif"}
	for n, b := range srcs {
		key := strings.Repeat(n, 40)
		os.WriteFile(filepath.Join(im.o.Dir, key+exts[n]), b, 0o644)
		rec := getImg(t, im, "/img/"+key+"?w=1000")
		if w, h := imageSize(t, rec.Body.Bytes()); rec.Code != 200 || w != 400 || h != 600 {
			t.Errorf("%s: код %d, %d×%d", exts[n], rec.Code, w, h)
		}
	}
}

// Оригинал не шире 400 — он и отдаётся; без w — оригинал, как раньше.
func TestThumbnailSmallOriginal(t *testing.T) {
	im := newImages(t, "")
	key := strings.Repeat("e", 40)
	p := filepath.Join(im.o.Dir, key+".jpg")
	writeJPEG(t, p, 300, 450)
	orig, _ := os.ReadFile(p)
	for _, u := range []string{"/img/" + key + "?w=400", "/img/" + key} {
		if rec := getImg(t, im, u); !bytes.Equal(rec.Body.Bytes(), orig) {
			t.Errorf("%s: не оригинал", u)
		}
	}
}

// Review Focus 3: битый файл — отдаётся как есть, миниатюра не пишется и не пробуется на каждый запрос.
func TestThumbnailBrokenServesOriginal(t *testing.T) {
	im := newImages(t, "")
	key := strings.Repeat("f", 40)
	os.WriteFile(filepath.Join(im.o.Dir, key+".jpg"), []byte("не картинка"), 0o644)
	for range 2 {
		if rec := getImg(t, im, "/img/"+key+"?w=400"); rec.Code != 200 || rec.Body.String() != "не картинка" {
			t.Fatalf("код %d, %q", rec.Code, rec.Body.String())
		}
	}
	if _, err := os.Stat(filepath.Join(im.o.Dir, key+".w400.jpg")); err == nil {
		t.Fatal("миниатюра битого файла")
	}
	if n := im.thumbFailures(); n != 1 {
		t.Fatalf("попыток %d", n)
	}
}

// Review Focus 5: миниатюра уходит вместе с оригиналом; сиротская старше суток — тоже; свежая сиротская остаётся.
func TestSweepRemovesThumbnails(t *testing.T) {
	dir := t.TempDir()
	im, err := NewImages(ImagesOptions{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	old, orphan, fresh := strings.Repeat("1", 40), strings.Repeat("2", 40), strings.Repeat("3", 40)
	for name, age := range map[string]time.Duration{old + ".jpg": 48 * time.Hour, old + ".w400.jpg": time.Hour,
		orphan + ".w400.jpg": 48 * time.Hour, fresh + ".w400.jpg": time.Hour} {
		p := filepath.Join(dir, name)
		os.WriteFile(p, []byte("x"), 0o644)
		os.Chtimes(p, now.Add(-age), now.Add(-age))
	}
	if _, err := im.Sweep(func(string) bool { return false }, now); err != nil {
		t.Fatal(err)
	}
	left, _ := os.ReadDir(dir)
	if len(left) != 1 || left[0].Name() != fresh+".w400.jpg" {
		var names []string
		for _, e := range left {
			names = append(names, e.Name())
		}
		t.Fatalf("осталось %v", names)
	}
}

func picture(w, h int) image.Image {
	m := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			m.Set(x, y, color.RGBA{uint8(x), uint8(y), 128, 255})
		}
	}
	return m
}

func writeJPEG(t *testing.T, path string, w, h int) {
	t.Helper()
	var b bytes.Buffer
	if err := jpeg.Encode(&b, picture(w, h), nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func encodePNG(t *testing.T, w, h int) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, picture(w, h)); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func encodeGIF(t *testing.T, w, h int) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := gif.Encode(&b, picture(w, h), nil); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func imageSize(t *testing.T, b []byte) (int, int) {
	t.Helper()
	c, _, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("не картинка: %v", err)
	}
	return c.Width, c.Height
}

func getImg(t *testing.T, im *Images, url string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle("GET /img/{key}", im.Handler())
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", url, nil))
	return rec
}

// Ревью 16А, Important 2: «постер» огромного размера (12000 × 12000 PNG — 440 КБ на диске, ~700 МБ в памяти при
// разборе) не разбирается целиком: размер — по заголовку, больше предела — отдаётся оригинал.
func TestThumbnailHugeSkipsDecode(t *testing.T) {
	im := newImages(t, "")
	key := strings.Repeat("9", 40)
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewGray(image.Rect(0, 0, 12000, 12000))); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(im.o.Dir, key+".png")
	os.WriteFile(p, b.Bytes(), 0o644)
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	rec := getImg(t, im, "/img/"+key+"?w=400")
	runtime.ReadMemStats(&after)
	if rec.Code != 200 || rec.Body.Len() != b.Len() {
		t.Fatalf("код %d, %d байт из %d — не оригинал", rec.Code, rec.Body.Len(), b.Len())
	}
	if grew := after.TotalAlloc - before.TotalAlloc; grew > 64<<20 {
		t.Fatalf("разобран целиком: +%d МБ", grew>>20)
	}
}

// Ревью 16А, Minor 4: обе очереди миниатюр заняты, а просьба уже отменена (листали дальше) — не ждать, отдать оригинал.
func TestThumbnailQueueHonoursCancel(t *testing.T) {
	im := newImages(t, "")
	key := strings.Repeat("8", 40)
	writeJPEG(t, filepath.Join(im.o.Dir, key+".jpg"), 1000, 1500)
	for range thumbAtOnce {
		im.thumbSem <- struct{}{}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	mux := http.NewServeMux()
	mux.Handle("GET /img/{key}", im.Handler())
	done := make(chan int, 1)
	go func() {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", "/img/"+key+"?w=400", nil).WithContext(ctx))
		done <- rec.Code
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("ждёт занятую очередь, хотя просьба отменена")
	}
}

// Ревью 16А, Minor 5: прозрачное в PNG — фоном постера (#1A1D21), а не чёрным.
func TestThumbnailTransparentBackground(t *testing.T) {
	im := newImages(t, "")
	key := strings.Repeat("7", 40)
	var b bytes.Buffer
	png.Encode(&b, image.NewNRGBA(image.Rect(0, 0, 800, 1200))) // целиком прозрачная
	os.WriteFile(filepath.Join(im.o.Dir, key+".png"), b.Bytes(), 0o644)
	rec := getImg(t, im, "/img/"+key+"?w=400")
	m, _, err := image.Decode(bytes.NewReader(rec.Body.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	r, g, bl, _ := m.At(200, 300).RGBA()
	if r>>8 < 0x10 || g>>8 < 0x14 || bl>>8 < 0x18 {
		t.Fatalf("цвет прозрачного: %d,%d,%d", r>>8, g>>8, bl>>8)
	}
}
