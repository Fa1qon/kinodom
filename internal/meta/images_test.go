package meta

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"kinodom/internal/netx"
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
	px, err := netx.NewProxy(proxy)
	if err != nil {
		t.Fatal(err)
	}
	im, err := NewImages(ImagesOptions{Dir: t.TempDir(), Proxy: px, Rate: 1000, AllowPrivate: true})
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

// Хостинги картинок отвечают 404 на User-Agent Go по умолчанию (fastpic, проверено вживую,
// исследование, раздел 13) — картинки качаются с UA браузера.
func TestFetchSendsBrowserUserAgent(t *testing.T) {
	pic := pngBytes(t)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.UserAgent(), "Go-http-client") {
			http.NotFound(w, r)
			return
		}
		w.Write(pic)
	}))
	t.Cleanup(s.Close)
	if _, err := newImages(t, "").Fetch(ctx, s.URL+"/p.jpg", Direct); err != nil {
		t.Fatalf("хостинг, который не пускает Go: %v", err)
	}
}

// Кэш чистится: ненужная картинка старше суток и обрывок .tmp старше часа удаляются; нужная,
// свежая, свежий обрывок и чужие файлы — остаются (хвост этапа 5b).
func TestImagesSweep(t *testing.T) {
	dir := t.TempDir()
	im, err := NewImages(ImagesOptions{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	old, kept, fresh := ImageKey("http://h/old.jpg"), ImageKey("http://h/kept.jpg"), ImageKey("http://h/fresh.jpg")
	files := map[string]time.Duration{
		old + ".jpg": 48 * time.Hour, kept + ".png": 48 * time.Hour, fresh + ".jpg": time.Hour,
		old + "-1.tmp": 2 * time.Hour, fresh + "-2.tmp": time.Minute, "readme.txt": 48 * time.Hour,
	}
	for name, age := range files {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, now.Add(-age), now.Add(-age)); err != nil {
			t.Fatal(err)
		}
	}
	removed, err := im.Sweep(func(k string) bool { return k == kept }, now)
	if err != nil || !slices.Equal(removed, []string{old}) {
		t.Fatalf("удалены %v, %v", removed, err)
	}
	left, _ := os.ReadDir(dir)
	var names []string
	for _, e := range left {
		names = append(names, e.Name())
	}
	want := []string{fresh + "-2.tmp", fresh + ".jpg", kept + ".png", "readme.txt"}
	slices.Sort(want)
	if !slices.Equal(names, want) {
		t.Fatalf("остались %v", names)
	}
}

// Одну картинку одновременно просят несколько раздач: у всех — ключ, ни одной ошибки «Access is
// denied» от второго переименования (ревью 5b, M4).
func TestConcurrentFetchOfSameImage(t *testing.T) {
	var hits atomic.Int32
	s := imageSite(t, &hits)
	im := newImages(t, "")
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Go(func() { _, errs[i] = im.Fetch(ctx, s.URL+"/p.png", Direct) })
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		t.Fatal(err)
	}
}

// Адрес, где не картинка, не качается снова: заглушку хостинга просят сотни раздач (ревью 5b, M4).
func TestNoImageAddressIsRemembered(t *testing.T) {
	var hits atomic.Int32
	s := imageSite(t, &hits)
	im := newImages(t, "")
	for range 3 {
		if _, err := im.Fetch(ctx, s.URL+"/page", Direct); !errors.Is(err, ErrNoImage) {
			t.Fatalf("заглушка: %v", err)
		}
	}
	if hits.Load() != 1 {
		t.Fatalf("заглушка скачана %d раз", hits.Load())
	}
}

// Заглушка хостинга (хвост Х6): одна и та же картинка с трёх разных адресов — это «Thumbnail
// Temporarily Unavailable» хостинга, а не постер: третий адрес — ErrNoImage, уже скачанные с тем же
// содержимым удаляются и отдаются каталогу (Stubbed), чтобы он снял их с раздач; настоящий постер
// принимается. Признанная заглушка помнится после перезапуска.
func TestStubImageDetected(t *testing.T) {
	stub := pngBytes(t)
	real := append(pngBytes(t), 0) // другое содержимое
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/real.png" {
			w.Write(real)
			return
		}
		w.Write(stub)
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	im, err := NewImages(ImagesOptions{Dir: dir, Rate: 1000, AllowPrivate: true, StubSources: PosterStubSources})
	if err != nil {
		t.Fatal(err)
	}
	a, errA := im.Fetch(ctx, srv.URL+"/a.png", Direct)
	b, errB := im.Fetch(ctx, srv.URL+"/b.png", Direct)
	if errA != nil || errB != nil {
		t.Fatalf("первые два адреса: %v %v", errA, errB)
	}
	if _, err := im.Fetch(ctx, srv.URL+"/c.png", Direct); !errors.Is(err, ErrNoImage) {
		t.Fatalf("третий адрес с той же картинкой: %v", err)
	}
	if got := im.Stubbed(); !slices.Contains(got, a) || !slices.Contains(got, b) {
		t.Fatalf("снятые ключи: %v, нужны %s %s", got, a, b)
	}
	if im.find(a) != "" || im.find(b) != "" {
		t.Fatal("файлы заглушки остались в кэше")
	}
	if _, err := im.Fetch(ctx, srv.URL+"/a.png", Direct); !errors.Is(err, ErrNoImage) {
		t.Fatalf("адрес заглушки снова: %v", err)
	}
	if _, err := im.Fetch(ctx, srv.URL+"/real.png", Direct); err != nil {
		t.Fatalf("настоящий постер: %v", err)
	}
	again, err := NewImages(ImagesOptions{Dir: dir, Rate: 1000, AllowPrivate: true, StubSources: PosterStubSources})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := again.Fetch(ctx, srv.URL+"/d.png", Direct); !errors.Is(err, ErrNoImage) {
		t.Fatalf("после перезапуска заглушка забыта: %v", err)
	}
}

// Заглушку признали, пока другой адрес с тем же содержимым ещё записывал файл: отданный ключ должен
// попасть в Stubbed (каталог снимет его с раздачи) или не отдаваться вовсе — иначе раздача остаётся с
// заглушкой навсегда (гонка, найдена нестабильным TestStubPosterReplacedByKinopoisk, 11b-А).
func TestStubRecognizedDuringConcurrentFetch(t *testing.T) {
	stub := pngBytes(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(stub) }))
	t.Cleanup(srv.Close)
	for round := range 60 {
		im, err := NewImages(ImagesOptions{Dir: t.TempDir(), Rate: 1000, AllowPrivate: true, StubSources: PosterStubSources})
		if err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		var mu sync.Mutex
		var got []string
		start := make(chan struct{})
		for _, p := range []string{"/a.png", "/b.png", "/c.png"} {
			wg.Go(func() {
				<-start
				if k, err := im.Fetch(ctx, srv.URL+p, Direct); err == nil {
					mu.Lock()
					got = append(got, k)
					mu.Unlock()
				}
			})
		}
		close(start)
		wg.Wait()
		dropped := im.Stubbed()
		for _, k := range got {
			if !slices.Contains(dropped, k) {
				t.Fatalf("круг %d: ключ %s заглушки отдан, но не снят (снятые %v)", round, k, dropped)
			}
		}
	}
}

// Логотипы каналов: часовые версии канала и зеркала ведут на один файл с разных адресов — это не
// заглушка хостинга. Без StubSources заглушки не распознаются и прежний stubs.txt не читается
// (ревью 11b-А: иначе логотип федерального канала пропадал навсегда).
func TestStubDetectionOffByDefault(t *testing.T) {
	logo := pngBytes(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(logo) }))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	sum := sha256.Sum256(logo)
	if err := os.WriteFile(filepath.Join(dir, stubsFile), []byte(hex.EncodeToString(sum[:])+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	im, err := NewImages(ImagesOptions{Dir: dir, Rate: 1000, AllowPrivate: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/perviy.png", "/perviy-plus2.png", "/perviy-plus4.png", "/perviy-plus6.png"} {
		if _, err := im.Fetch(ctx, srv.URL+p, Direct); err != nil {
			t.Fatalf("логотип %s: %v", p, err)
		}
	}
	if got := im.Stubbed(); len(got) != 0 {
		t.Fatalf("сняты как заглушки: %v", got)
	}
}

// Х1: Accept картинок без AVIF — его разбирать нечем (иначе хостинг отдаст AVIF и постера не будет).
func TestImagesAcceptNoAVIF(t *testing.T) {
	pic := pngBytes(t)
	var accept atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		accept.Store(r.Header.Get("Accept"))
		w.Write(pic)
	}))
	t.Cleanup(srv.Close)
	im := newImages(t, "")
	if _, err := im.Fetch(ctx, srv.URL+"/p.png", Direct); err != nil {
		t.Fatal(err)
	}
	if a, _ := accept.Load().(string); strings.Contains(a, "avif") || a == "" {
		t.Fatalf("Accept %q", a)
	}
}

// Неудача сети или ответ не 200 (хвост Х29): с FailFor адрес не запрашивается снова столько времени —
// логотип, которого нет, не качается на каждой перерисовке списка каналов; потом — снова.
func TestFailedFetchRemembered(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	im, err := NewImages(ImagesOptions{Dir: t.TempDir(), Rate: 1000, AllowPrivate: true, FailFor: 200 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err := im.Fetch(ctx, srv.URL+"/logo.png", Direct); err == nil {
			t.Fatal("ответ 500 — не ошибка")
		}
	}
	if hits.Load() != 1 {
		t.Fatalf("неудачный адрес запрошен %d раз за FailFor", hits.Load())
	}
	time.Sleep(250 * time.Millisecond)
	im.Fetch(ctx, srv.URL+"/logo.png", Direct)
	if hits.Load() != 2 {
		t.Fatalf("после FailFor адрес не запрошен снова: %d", hits.Load())
	}
}

// Адреса этого ПК и домашней сети из описаний раздач не скачиваются — ни прямо, ни по имени,
// которое указывает в домашнюю сеть; сам прокси в домашней сети при этом работает (ревью 5b, M10).
func TestPrivateAddressesAreNotFetched(t *testing.T) {
	var hits atomic.Int32
	s := imageSite(t, &hits)
	im, err := NewImages(ImagesOptions{Dir: t.TempDir(), Rate: 1000})
	if err != nil {
		t.Fatal(err)
	}
	_, port, _ := strings.Cut(strings.TrimPrefix(s.URL, "http://"), ":")
	for _, u := range []string{"http://192.168.1.1/p.png", "http://localhost:" + port + "/p.png", "http://[::1]:" + port + "/p.png"} {
		if _, err := im.Fetch(ctx, u, ViaProxy); !errors.Is(err, ErrNoImage) {
			t.Errorf("%s: %v", u, err)
		}
	}
	if _, err := im.Fetch(ctx, s.URL+"/p.png", Direct); !errors.Is(err, ErrNoImage) {
		t.Errorf("127.0.0.1 напрямую: %v", err)
	}
	if hits.Load() != 0 {
		t.Fatalf("до домашней сети дошло %d запросов", hits.Load())
	}
	pic := pngBytes(t)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(pic) }))
	t.Cleanup(proxy.Close)
	px, _ := netx.NewProxy(proxy.URL) // прокси — на этом ПК
	viaProxy, err := NewImages(ImagesOptions{Dir: t.TempDir(), Rate: 1000, Proxy: px})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := viaProxy.Fetch(ctx, "http://images.example/p.png", ViaProxy); err != nil {
		t.Fatalf("через прокси в домашней сети: %v", err)
	}
}

// С прокси соединение идёт только с самим прокси, и проверка при соединении не видит, куда ведёт
// редирект: хостинг из описания раздачи отвечает 302 на адрес домашней сети — туда не ходим
// (финальное ревью 7a, M10).
func TestRedirectToPrivateAddressIsNotFollowed(t *testing.T) {
	pic := pngBytes(t)
	var private atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Hostname() == "192.168.1.1" {
			private.Add(1)
			w.Write(pic)
			return
		}
		http.Redirect(w, r, "http://192.168.1.1/admin.png", http.StatusFound)
	}))
	t.Cleanup(proxy.Close)
	px, _ := netx.NewProxy(proxy.URL)
	im, err := NewImages(ImagesOptions{Dir: t.TempDir(), Rate: 1000, Proxy: px})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := im.Fetch(ctx, "http://images.example/p.png", ViaProxy); !errors.Is(err, ErrNoImage) {
		t.Errorf("редирект в домашнюю сеть: %v", err)
	}
	if private.Load() != 0 {
		t.Fatalf("до домашней сети дошло %d запросов", private.Load())
	}
}
