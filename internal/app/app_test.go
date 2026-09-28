package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"

	"kinodom/internal/api"
	"kinodom/internal/config"
	"kinodom/internal/meta"
	"kinodom/internal/store"
	"kinodom/internal/supervisor"
	"kinodom/internal/torrents"
	"kinodom/internal/torrents/torrenttest"
)

// startAppRaw поднимает сервер и ждёт только HTTP; модули могут ещё стартовать.
func startAppRaw(t *testing.T, o Options) *App {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	a, err := New(ctx, o)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { a.Run(ctx); close(done) }()
	t.Cleanup(func() {
		cancel()
		<-done
		a.Close()
	})
	select {
	case <-a.API.Ready():
	case <-time.After(5 * time.Second):
		t.Fatal("API не поднялся за 5 с")
	}
	return a
}

// startAppWith поднимает сервер и ждёт, пока все модули будут готовы (Ready).
func startAppWith(t *testing.T, o Options) *App {
	t.Helper()
	a := startAppRaw(t, o)
	waitAllRunning(t, a) // модули готовы — их маршруты уже не отвечают 503
	return a
}

// startApp — сервер во временной папке, на случайном порту, с офлайн-движком торрентов.
func startApp(t *testing.T) *App {
	return startAppWith(t, Options{Home: t.TempDir(), ListenAddr: "127.0.0.1:0", Offline: true, DownloadsDir: t.TempDir()})
}

func getJSON(t *testing.T, url string, v any) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: код %d", url, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatal(err)
	}
}

// waitAllRunning ждёт, пока все включённые модули перейдут в running.
func waitAllRunning(t *testing.T, a *App) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		ok := true
		for _, st := range a.Sup.Status() {
			if st.State != supervisor.StateRunning && st.State != supervisor.StateDisabled {
				ok = false
			}
		}
		if ok {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("не все модули работают: %+v", a.Sup.Status())
}

func TestAllModulesTogether(t *testing.T) {
	a := startApp(t)
	waitAllRunning(t, a)
	var st struct {
		Problems []any               `json:"problems"`
		Modules  []supervisor.Status `json:"modules"`
	}
	getJSON(t, "http://"+a.API.Addr()+"/api/v1/status", &st)
	names := map[string]supervisor.State{}
	for _, m := range st.Modules {
		names[m.Name] = m.State
	}
	if names["api"] != supervisor.StateRunning {
		t.Fatalf("модули в /status: %+v", st.Modules)
	}
	if st.Problems == nil {
		t.Fatal("problems должен быть [], а не null")
	}
}

func TestModuleCanBeDisabledBySetting(t *testing.T) {
	ctx := context.Background()
	a, err := New(ctx, Options{Home: t.TempDir(), ListenAddr: "127.0.0.1:0", Offline: true, DownloadsDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if !a.ModuleEnabled(ctx, "iptv") {
		t.Fatal("по умолчанию модуль включён")
	}
	if err := a.DB.SetSetting(ctx, "modules.iptv.enabled", "false"); err != nil {
		t.Fatal(err)
	}
	if a.ModuleEnabled(ctx, "iptv") {
		t.Fatal("настройка modules.iptv.enabled=false не выключила модуль")
	}
}

// Порт может занять чужая программа по-разному: только IPv4, только 127.0.0.1, только ::1
// или двухстеково. В любом случае сервер не должен стартовать «наполовину».
func TestBusyPortIsDetectedInEveryForm(t *testing.T) {
	forms := []struct{ network, addr string }{
		{"tcp4", "0.0.0.0:0"},
		{"tcp4", "127.0.0.1:0"},
		{"tcp6", "[::1]:0"},
		{"tcp", ":0"},
	}
	for _, f := range forms {
		ln, err := net.Listen(f.network, f.addr)
		if err != nil {
			t.Logf("%s %s: не удалось занять порт (%v), пропускаю", f.network, f.addr, err)
			continue
		}
		port := ln.Addr().(*net.TCPAddr).Port
		a, err := New(context.Background(), Options{Home: t.TempDir(), ListenAddr: fmt.Sprintf(":%d", port)})
		ln.Close()
		if err == nil {
			a.Close()
			t.Errorf("%s %s: порт %d занят, а сервер запустился", f.network, f.addr, port)
			continue
		}
		if !errors.Is(err, api.ErrPortBusy) || !strings.Contains(err.Error(), "занят") {
			t.Errorf("%s %s: ожидалась ошибка «порт занят», получено %v", f.network, f.addr, err)
		}
	}
}

// Служба работает без консоли: причина отказа запуска должна попасть в журнал.
func TestStartupFailureIsWrittenToLog(t *testing.T) {
	ctx := context.Background()
	cases := map[string]func(t *testing.T, home string){
		"новее": func(t *testing.T, home string) { // база от более новой версии Kinodom
			db, err := store.Open(ctx, config.NewPaths(home).DB)
			if err != nil {
				t.Fatal(err)
			}
			db.W.Exec("PRAGMA user_version = 999")
			db.Close()
		},
		"kinodom.json": func(t *testing.T, home string) {
			os.WriteFile(filepath.Join(home, "kinodom.json"), []byte("{"), 0o644)
		},
	}
	for want, prepare := range cases {
		home := t.TempDir()
		prepare(t, home)
		if a, err := New(ctx, Options{Home: home, ListenAddr: "127.0.0.1:0"}); err == nil {
			a.Close()
			t.Fatalf("%s: запуск должен был отказать", want)
		}
		data, err := os.ReadFile(filepath.Join(config.NewPaths(home).Logs, "kinodom.log"))
		if err != nil || !strings.Contains(string(data), "Kinodom не запустился") || !strings.Contains(string(data), want) {
			t.Fatalf("%s: в журнале нет причины отказа (%v): %s", want, err, data)
		}
	}
}

func postJSON(t *testing.T, url string, in, out any) {
	t.Helper()
	b, _ := json.Marshal(in)
	resp, err := http.Post(url, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST %s: код %d: %s", url, resp.StatusCode, body)
	}
	if out != nil {
		json.NewDecoder(resp.Body).Decode(out)
	}
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("не дождались: %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// openAndBuffer открывает раздачу через API, подключает раздающего и ждёт буфер.
func openAndBuffer(t *testing.T, a *App, mi metainfo.MetaInfo, seeder *torrent.Client) string {
	t.Helper()
	base := "http://" + a.API.Addr()
	raw, _ := bencode.Marshal(mi)
	var opened struct{ Hash string }
	postJSON(t, base+"/api/v1/torrents", map[string]any{"torrent": raw}, &opened)
	if seeder != nil {
		tt, _ := a.Torrents.Engine().Client().Torrent(mi.HashInfoBytes())
		torrenttest.Connect(t, tt, seeder)
	}
	var st torrents.TorrentStatus
	waitUntil(t, "список файлов", func() bool {
		getJSON(t, base+"/api/v1/torrents/"+opened.Hash, &st)
		return st.State == torrents.StateReady
	})
	fileURL := fmt.Sprintf("%s/api/v1/torrents/%s/files/%d", base, opened.Hash, st.Files[0].Index)
	postJSON(t, fileURL+"/prepare", struct{}{}, nil)
	var fs struct {
		State     string
		StreamURL string `json:"streamUrl"`
	}
	waitUntil(t, "буфер готов", func() bool { getJSON(t, fileURL, &fs); return fs.State == "ready" })
	return fs.StreamURL
}

func readAll(t *testing.T, url string) []byte {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestTorrentFromOpenToStreamThroughAPI(t *testing.T) {
	a := startApp(t)
	src := t.TempDir()
	mi, root := torrenttest.MakeTorrent(t, src, "Фильм про космос.mkv", 64<<10, torrenttest.File{Path: "Фильм про космос.mkv", Size: 3 << 20})
	want, _ := os.ReadFile(root)
	seeder, _ := torrenttest.NewSeeder(t, src, mi)
	streamURL := openAndBuffer(t, a, mi, seeder)
	if got := readAll(t, streamURL); !bytes.Equal(got, want) {
		t.Fatalf("поток отдал %d байт, не совпадает с исходным файлом", len(got))
	}
	waitAllRunning(t, a) // остальные модули не задело
}

func TestRestartKeepsDownloadedFileWithoutPeers(t *testing.T) {
	home, downloads := t.TempDir(), t.TempDir()
	opts := Options{Home: home, ListenAddr: "127.0.0.1:0", Offline: true, DownloadsDir: downloads}
	src := t.TempDir()
	mi, root := torrenttest.MakeTorrent(t, src, "film.mkv", 64<<10, torrenttest.File{Path: "film.mkv", Size: 2 << 20})
	want, _ := os.ReadFile(root)
	seeder, _ := torrenttest.NewSeeder(t, src, mi)

	// Первый запуск: скачать файл целиком.
	ctx1, cancel1 := context.WithCancel(context.Background())
	a1, err := New(ctx1, opts)
	if err != nil {
		t.Fatal(err)
	}
	done1 := make(chan struct{})
	go func() { a1.Run(ctx1); close(done1) }()
	<-a1.API.Ready()
	waitAllRunning(t, a1)
	openAndBuffer(t, a1, mi, seeder)
	tt, _ := a1.Torrents.Engine().Client().Torrent(mi.HashInfoBytes())
	waitUntil(t, "файл скачан целиком", func() bool { return tt.Files()[0].BytesCompleted() == int64(len(want)) })
	cancel1()
	<-done1
	a1.Close()

	// Второй запуск: та же папка, раздающего не подключаем — файл открывается из скачанного.
	a2 := startAppWith(t, opts)
	streamURL := openAndBuffer(t, a2, mi, nil)
	if got := readAll(t, streamURL); !bytes.Equal(got, want) {
		t.Fatal("после перезапуска поток отдал не тот файл")
	}
}

func problemsOf(t *testing.T, a *App) string {
	t.Helper()
	var st struct{ Problems []store.Problem }
	getJSON(t, "http://"+a.API.Addr()+"/api/v1/status", &st)
	var texts []string
	for _, p := range st.Problems {
		texts = append(texts, p.Text)
	}
	return strings.Join(texts, " | ")
}

func TestBadProxySettingDoesNotStopServer(t *testing.T) {
	home := t.TempDir()
	db, err := store.Open(context.Background(), config.NewPaths(home).DB)
	if err != nil {
		t.Fatal(err)
	}
	db.SetSetting(context.Background(), "proxy.trackers", "127.0.0.1:1080")
	db.Close()
	a := startAppWith(t, Options{Home: home, ListenAddr: "127.0.0.1:0", Offline: true, DownloadsDir: t.TempDir()})
	if a.Torrents == nil {
		t.Fatal("торренты должны работать и без прокси")
	}
	if p := problemsOf(t, a); !strings.Contains(p, "socks5://") {
		t.Fatalf("нет понятной проблемы про прокси: %q", p)
	}
	waitAllRunning(t, a)
}

// Папка загрузок недоступна при старте (например, USB-диск ещё не подключился): сервер жив,
// маршруты торрентов отвечают 503 в JSON, в «Состоянии» — проблема. Когда папка появляется,
// сторож перезапускает модуль и торренты оживают сами, без перезапуска службы.
func TestUnusableDownloadsDirRecoversWhenFolderAppears(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "file")
	os.WriteFile(blocker, nil, 0o644)
	a := startAppRaw(t, Options{Home: t.TempDir(), ListenAddr: "127.0.0.1:0", Offline: true, DownloadsDir: filepath.Join(blocker, "sub")})
	waitUntil(t, "проблема про торренты", func() bool { return strings.Contains(problemsOf(t, a), "Торренты не работают") })

	resp, err := http.Get("http://" + a.API.Addr() + "/api/v1/torrents/" + strings.Repeat("0", 40))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable || !strings.Contains(string(body), "временно недоступен") {
		t.Fatalf("пока движка нет, маршрут должен отвечать 503 в JSON: %d %s", resp.StatusCode, body)
	}

	if err := os.Remove(blocker); err != nil { // «диск подключили»
		t.Fatal(err)
	}
	waitUntil(t, "торренты заработали сами", func() bool { return a.Sup.IsRunning("torrents") })
	if p := problemsOf(t, a); strings.Contains(p, "Торренты не работают") {
		t.Fatalf("проблема не снята: %q", p)
	}
}

// Рейтинги и картинки вместе с остальными модулями: ключ Кинопоиска из настроек, очередь даёт
// рейтинг; скачанная картинка отдаётся по /img/{key} (спека, раздел 8).
func TestRatingsAndImagesTogether(t *testing.T) {
	kp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-KEY") != "k" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/v1/api_keys/k":
			io.WriteString(w, `{"totalQuota":{"value":-1,"used":0},"dailyQuota":{"value":500,"used":0},"accountType":"FREE"}`)
		case "/api/v2.2/films/301":
			io.WriteString(w, `{"kinopoiskId":301,"imdbId":"tt0133093","nameRu":"Матрица","nameOriginal":"The Matrix","year":1999,"type":"FILM","ratingKinopoisk":8.5,"ratingImdb":8.7}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(kp.Close)
	var pic bytes.Buffer
	png.Encode(&pic, image.NewRGBA(image.Rect(0, 0, 1, 1)))
	img := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(pic.Bytes()) }))
	t.Cleanup(img.Close)

	home := t.TempDir()
	db, err := store.Open(context.Background(), config.NewPaths(home).DB)
	if err != nil {
		t.Fatal(err)
	}
	db.SetSetting(context.Background(), "kinopoisk.key", "k")
	db.Close()
	a := startAppWith(t, Options{Home: home, ListenAddr: "127.0.0.1:0", Offline: true, DownloadsDir: t.TempDir(), KinopoiskAPI: kp.URL})

	ctx := context.Background()
	if err := a.Ratings.Enqueue(ctx, 1, meta.Item{Release: "rutor:1", KinopoiskID: 301}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		m, err := a.Ratings.For(ctx, []string{"rutor:1"})
		if err != nil {
			t.Fatal(err)
		}
		if m["rutor:1"].Kinopoisk == 8.5 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("рейтинга нет за 5 с: %+v", m)
		}
		time.Sleep(20 * time.Millisecond)
	}

	key, err := a.Images.Fetch(ctx, img.URL+"/poster.png", meta.Direct)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get("http://" + a.API.Addr() + "/img/" + key)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "image/png" || !bytes.Equal(body, pic.Bytes()) {
		t.Fatalf("/img: код %d, тип %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
}
