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
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"

	"kinodom/internal/api"
	"kinodom/internal/catalog"
	"kinodom/internal/config"
	"kinodom/internal/meta"
	"kinodom/internal/settings"
	"kinodom/internal/source/rutor/rutortest"
	"kinodom/internal/source/rutracker"
	"kinodom/internal/source/rutracker/rutrackertest"
	"kinodom/internal/store"
	"kinodom/internal/supervisor"
	"kinodom/internal/torrents"
	"kinodom/internal/torrents/torrenttest"
)

// startAppRaw поднимает сервер и ждёт только HTTP; модули могут ещё стартовать.
func startAppRaw(t *testing.T, o Options) *App {
	t.Helper()
	if o.Trackers.RutorMirrors == nil && o.Trackers.RutrackerMirrors == nil && !o.Trackers.NoEdge {
		// Адресов трекеров нет — трекеры выключены и в сеть не ходят (этап 11a); Edge не нужен.
		o.Trackers = Trackers{NoEdge: true, Rate: 1000}
	}
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

// offlineTrackers — трекеры по адресу закрытого локального сервера: каталог работает вместе с
// остальными модулями, но тесты не ходят в интернет.
func offlineTrackers(t *testing.T) Trackers {
	t.Helper()
	dead := httptest.NewServer(http.NotFoundHandler())
	u := dead.URL
	dead.Close()
	return Trackers{RutorMirrors: []string{u}, RutorDownload: u, RutrackerMirrors: []string{u}, RutrackerAPI: u,
		RutrackerFeed: u, NoEdge: true, Rate: 1000}
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
	opts := Options{Home: home, ListenAddr: "127.0.0.1:0", Offline: true, DownloadsDir: downloads, Trackers: offlineTrackers(t)}
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
	a := startAppWith(t, Options{Home: home, ListenAddr: "127.0.0.1:0", Offline: true, DownloadsDir: t.TempDir(), KinopoiskAPI: kp.URL,
		LocalImages: true})

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

// Каталог вместе с остальными модулями на фейковых трекерах: трекеры и картинки раздач — через
// прокси из настроек, логин Rutracker — из Options.Settings (не в базу); топ Rutor с названиями,
// догрузка страницы, постера и .torrent; поиск по обоим трекерам (спека, разделы 5, 7).
func TestCatalogTogether(t *testing.T) {
	rutor := rutortest.NewServer(t)
	// Постер раздачи — по http: фейковый прокси не умеет CONNECT для https. Что взят он, а не
	// запасной постер Кинопоиска, показывает ключ картинки у карточки.
	topic := bytes.ReplaceAll(rutortest.Page(t, "torrent_1077013.html"), []byte("https://"), []byte("http://"))
	rutor.Override = func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/torrent/1077013" {
			return false
		}
		w.Header().Set("Content-Type", "text/html; charset=UTF-8")
		w.Write(topic)
		return true
	}
	rt := rutrackertest.NewServer(t)
	rt.Login, rt.Password = "user", "pass"
	var pic bytes.Buffer
	png.Encode(&pic, image.NewRGBA(image.Rect(0, 0, 1, 1)))
	var viaProxy atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		viaProxy.Add(1)
		if !strings.HasPrefix(r.URL.Host, "127.0.0.1") { // картинка с внешнего хостинга
			w.Write(pic.Bytes())
			return
		}
		out, err := http.NewRequest(r.Method, r.URL.String(), r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		out.Header = r.Header.Clone()
		resp, err := http.DefaultTransport.RoundTrip(out)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		for k, v := range resp.Header {
			w.Header()[k] = v
		}
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
	}))
	t.Cleanup(proxy.Close)

	home := t.TempDir()
	db, err := store.Open(context.Background(), config.NewPaths(home).DB)
	if err != nil {
		t.Fatal(err)
	}
	db.SetSetting(context.Background(), "proxy.trackers", proxy.URL)
	db.SetSetting(context.Background(), "catalog.categories", "rutor:12,rutracker:2076")
	db.Close()
	kp := httptest.NewServer(http.NotFoundHandler()) // рейтинги без ключа и постеры — не в настоящий Кинопоиск
	t.Cleanup(kp.Close)
	a := startAppWith(t, Options{Home: home, ListenAddr: "127.0.0.1:0", Offline: true, DownloadsDir: t.TempDir(),
		KinopoiskAPI: kp.URL, Settings: map[string]string{"rutracker.login": "user", "rutracker.password": "pass"},
		Trackers: Trackers{RutorMirrors: []string{rutor.Mirror.URL}, RutorDownload: rutor.Download.URL,
			RutrackerMirrors: []string{rt.Forum.URL}, RutrackerAPI: rt.API.URL, RutrackerFeed: rt.Feed.URL, NoEdge: true, Rate: 1000}})

	ctx := context.Background()
	deadline := time.Now().Add(10 * time.Second)
	for {
		es, _, err := a.Catalog.List(ctx, catalog.ListOptions{Tracker: "rutor", Limit: 200})
		if err != nil {
			t.Fatal(err)
		}
		i := slices.IndexFunc(es, func(e catalog.Entry) bool { return e.TopicID == "1077013" })
		poster := meta.ImageKey("http://i8.imageban.ru/out/2026/03/14/8e3986dbaaf516b0a8fbc3d6928911f6.jpg")
		if i >= 0 && strings.HasPrefix(es[i].Title, "Динозавры") && es[i].ImageKey == poster {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("раздача 1077013 не догрузилась за 10 с (карточек %d, 1077013 — №%d): %+v", len(es), i, es)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if viaProxy.Load() == 0 {
		t.Fatal("трекеры и картинки ходили мимо прокси")
	}
	var st catalog.SearchState
	for deadline := time.Now().Add(10 * time.Second); !st.Complete; time.Sleep(50 * time.Millisecond) {
		if st, err = a.Catalog.Search(ctx, "космос"); err != nil {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("поиск не закончился: %+v", st.Trackers)
		}
	}
	if st.Trackers["rutor"] != catalog.SearchOK || st.Trackers["rutracker"] != catalog.SearchOK || len(st.Results) == 0 {
		t.Fatalf("поиск: %+v, найдено %d", st.Trackers, len(st.Results))
	}
	var withLogin string
	if v, _, _ := a.DB.Setting(ctx, "rutracker.login"); v != "" {
		withLogin = v
	}
	if withLogin != "" {
		t.Fatal("логин из Options.Settings попал в базу")
	}
}

// Папка загрузок на сетевом диске: торренты не запускаются, в «Состоянии» — почему (хвост этапа 2).
func TestNetworkDownloadsDirIsExplained(t *testing.T) {
	a := startAppRaw(t, Options{Home: t.TempDir(), ListenAddr: "127.0.0.1:0", Offline: true, DownloadsDir: `\\kinodom-nas\video`})
	waitUntil(t, "проблема про сетевой диск", func() bool { return strings.Contains(problemsOf(t, a), "на сетевом диске") })
}

// Правила хранения — из настроек; запрет сна подключён к торрентам (спека, разделы 9 и 15).
func TestStoragePolicyFromSettings(t *testing.T) {
	home := t.TempDir()
	db, err := store.Open(context.Background(), config.NewPaths(home).DB)
	if err != nil {
		t.Fatal(err)
	}
	db.SetSetting(context.Background(), "torrents.keepDays", "3")
	db.SetSetting(context.Background(), "torrents.minFreeGB", "7")
	db.Close()
	a := startAppWith(t, Options{Home: home, ListenAddr: "127.0.0.1:0", Offline: true, DownloadsDir: t.TempDir()})
	p := a.Torrents.Policy()
	if p.KeepFor != 3*24*time.Hour || p.MinFree != 7<<30 || p.MaxSeeding != 10 {
		t.Fatalf("правила %+v", p)
	}
	if a.Power == nil {
		t.Fatal("запрет сна не создан")
	}
}

func putJSON(t *testing.T, url string, in any) (int, string) {
	t.Helper()
	b, _ := json.Marshal(in)
	req, _ := http.NewRequest(http.MethodPut, url, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

// Настройки из пульта действуют сразу, без перезапуска: правила хранения, ключ Кинопоиска, папка
// загрузок. Пароль и ключ в ответ не попадают; неверная папка — отказ, в базу ничего не пишется
// (спека этапа 7, раздел 5.1).
func TestSettingsApplyWithoutRestart(t *testing.T) {
	kp := httptest.NewServer(http.NotFoundHandler()) // новый ключ проверяется — не в настоящий Кинопоиск
	t.Cleanup(kp.Close)
	a := startAppWith(t, Options{Home: t.TempDir(), ListenAddr: "127.0.0.1:0", Offline: true, DownloadsDir: t.TempDir(), KinopoiskAPI: kp.URL})
	ctx := context.Background()
	url := "http://" + a.API.Addr() + "/api/v1/settings"
	if code, body := putJSON(t, url, map[string]any{"storage": map[string]any{"keepDays": 3, "minFreeGB": 0, "keepBehind": 2}}); code != 200 {
		t.Fatalf("хранение: %d %s", code, body)
	}
	if p := a.Torrents.Policy(); p.KeepFor != 3*24*time.Hour || p.MinFree != 0 || p.KeepBehind != 2 {
		t.Fatalf("правила хранения не применились: %+v", p)
	}
	code, body := putJSON(t, url, map[string]any{"kinopoisk": map[string]any{"key": "key-SECRET"},
		"rutracker": map[string]any{"login": "user", "password": "pass-SECRET"}})
	if code != 200 || strings.Contains(body, "SECRET") {
		t.Fatalf("ключ и пароль: %d %s", code, body)
	}
	if !a.kp.HasKey() {
		t.Fatal("ключ Кинопоиска не дошёл до клиента")
	}
	// Х22: ключ стирается пустым значением — Кинопоиск работает без токена, проблема ключа снята.
	a.DB.SetProblem(ctx, meta.ProblemKinopoiskKey, meta.ErrBadKey.Error())
	if code, body := putJSON(t, url, map[string]any{"kinopoisk": map[string]any{"key": ""}}); code != 200 {
		t.Fatalf("стереть ключ: %d %s", code, body)
	}
	if a.kp.HasKey() {
		t.Fatal("ключ не стёрся")
	}
	if ps, _ := a.DB.Problems(ctx); slices.ContainsFunc(ps, func(p store.Problem) bool { return p.ID == meta.ProblemKinopoiskKey }) {
		t.Fatal("проблема ключа осталась")
	}
	putJSON(t, url, map[string]any{"kinopoisk": map[string]any{"key": "key-SECRET"}})
	blocker := filepath.Join(t.TempDir(), "file")
	os.WriteFile(blocker, nil, 0o644)
	code, body = putJSON(t, url, map[string]any{"storage": map[string]any{"downloadsDir": filepath.Join(blocker, "sub")}})
	if code != http.StatusBadRequest || !strings.Contains(body, "Папка загрузок") {
		t.Fatalf("неверная папка: %d %s", code, body)
	}
	if _, ok, _ := a.DB.Setting(ctx, "downloads.dir"); ok {
		t.Fatal("неверная папка записана в базу")
	}
	newDir := t.TempDir()
	if code, body := putJSON(t, url, map[string]any{"storage": map[string]any{"downloadsDir": newDir}}); code != 200 {
		t.Fatalf("новая папка: %d %s", code, body)
	}
	if got := a.Torrents.Engine().DownloadsDir(); got != newDir {
		t.Fatalf("движок качает в %s, а не в %s", got, newDir)
	}
	if code, body := putJSON(t, url, map[string]any{"catalog": map[string]any{"preferredFormat": "MKV"}}); code != 200 {
		t.Fatalf("формат в приоритете: %d %s", code, body)
	}
	if got := a.Catalog.PreferredFormat(); got != "MKV" {
		t.Fatalf("формат в приоритете не дошёл до каталога: %q", got)
	}
	var v settings.View
	getJSON(t, url, &v)
	if !v.Rutracker.PasswordSet || v.Rutracker.Login != "user" || !v.Kinopoisk.KeySet || v.Storage.KeepDays != 3 ||
		v.Storage.MinFreeGB != 0 || v.Storage.DownloadsDir != newDir || v.Catalog.PreferredFormat != "MKV" {
		t.Fatalf("GET /settings: %+v", v)
	}
}

// countingProxy — HTTP-прокси, который считает запросы и пересылает их дальше.
func countingProxy(t *testing.T, hits *atomic.Int32) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		out, err := http.NewRequest(r.Method, r.URL.String(), r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		out.Header = r.Header.Clone()
		resp, err := http.DefaultTransport.RoundTrip(out)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		for k, v := range resp.Header {
			w.Header()[k] = v
		}
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
	}))
	t.Cleanup(s.Close)
	return s
}

// Прокси сменили в пульте — трекеры сразу идут через новый, без перезапуска (спека этапа 7, 5.2).
func TestProxyChangeWithoutRestart(t *testing.T) {
	rutor := rutortest.NewServer(t)
	var first, second atomic.Int32
	p1, p2 := countingProxy(t, &first), countingProxy(t, &second)
	home := t.TempDir()
	db, err := store.Open(context.Background(), config.NewPaths(home).DB)
	if err != nil {
		t.Fatal(err)
	}
	db.SetSetting(context.Background(), "proxy.trackers", p1.URL)
	db.SetSetting(context.Background(), "catalog.categories", "rutor:12")
	db.Close()
	kp := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(kp.Close)
	a := startAppWith(t, Options{Home: home, ListenAddr: "127.0.0.1:0", Offline: true, DownloadsDir: t.TempDir(), KinopoiskAPI: kp.URL,
		Trackers: Trackers{RutorMirrors: []string{rutor.Mirror.URL}, RutorDownload: rutor.Download.URL,
			RutrackerMirrors: []string{"http://" + closedAddr(t)}, RutrackerAPI: "http://" + closedAddr(t),
			RutrackerFeed: "http://" + closedAddr(t), NoEdge: true, Rate: 1000}})
	waitUntil(t, "каталог через первый прокси", func() bool { return first.Load() > 0 })
	u := strings.TrimPrefix(p2.URL, "http://")
	if code, body := putJSON(t, "http://"+a.API.Addr()+"/api/v1/settings", map[string]any{"proxy": map[string]any{"type": "http", "address": u}}); code != 200 {
		t.Fatalf("смена прокси: %d %s", code, body)
	}
	a.Catalog.Refresh()
	waitUntil(t, "каталог через второй прокси", func() bool { return second.Load() > 0 })
	// Запрос, начатый до смены, мог прийти к первому прокси уже после неё: сравниваем со следующим
	// обновлением, когда таких запросов в пути нет.
	before, next := first.Load(), second.Load()
	a.Catalog.Refresh()
	waitUntil(t, "снова через второй прокси", func() bool { return second.Load() > next })
	if first.Load() != before {
		t.Fatalf("после смены через первый прокси прошло ещё %d запросов", first.Load()-before)
	}
}

// closedAddr — адрес, где никто не слушает.
func closedAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

// Вход Rutracker заблокирован (неверный пароль) — проблема rutracker.login; новый пароль в пульте
// снимает её, «Войти» входит сразу (спека этапа 7, раздел 5.3; хвост 5c).
func TestRutrackerLoginProblemAndRelogin(t *testing.T) {
	rt := rutrackertest.NewServer(t)
	rt.Login, rt.Password = "user", "right"
	kp := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(kp.Close)
	dead := "http://" + closedAddr(t)
	a := startAppWith(t, Options{Home: t.TempDir(), ListenAddr: "127.0.0.1:0", Offline: true, DownloadsDir: t.TempDir(), KinopoiskAPI: kp.URL,
		Settings: map[string]string{"rutracker.login": "user", "rutracker.password": "wrong", "catalog.categories": "rutor:12"},
		Trackers: Trackers{RutorMirrors: []string{dead}, RutorDownload: dead, RutrackerMirrors: []string{rt.Forum.URL},
			RutrackerAPI: rt.API.URL, RutrackerFeed: rt.Feed.URL, NoEdge: true, Rate: 1000}})
	login := "http://" + a.API.Addr() + "/api/v1/sources/rutracker/login"
	var st rutracker.LoginInfo
	postJSON(t, login, struct{}{}, &st)
	if st.State != rutracker.LoginBlocked {
		t.Fatalf("неверный пароль: %+v", st)
	}
	if p := problemsOf(t, a); !strings.Contains(p, "Rutracker: неверный логин или пароль") {
		t.Fatalf("нет проблемы входа: %q", p)
	}
	if code, body := putJSON(t, "http://"+a.API.Addr()+"/api/v1/settings", map[string]any{"rutracker": map[string]any{"password": "right"}}); code != 200 {
		t.Fatalf("новый пароль: %d %s", code, body)
	}
	if p := problemsOf(t, a); strings.Contains(p, "Rutracker: неверный") {
		t.Fatalf("проблема не снята после смены пароля: %q", p)
	}
	postJSON(t, login, struct{}{}, &st)
	if st.State != rutracker.LoginOK {
		t.Fatalf("«Войти» с верным паролем: %+v", st)
	}
}

// Разделы каталога из пульта: проверка по дереву трекера, запись «раздел со всеми подразделами»,
// дерево для настроек (спека этапа 7, раздел 5.4).
func TestCatalogSectionsFromSettings(t *testing.T) {
	rt := rutrackertest.NewServer(t)
	kp := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(kp.Close)
	dead := "http://" + closedAddr(t)
	a := startAppWith(t, Options{Home: t.TempDir(), ListenAddr: "127.0.0.1:0", Offline: true, DownloadsDir: t.TempDir(), KinopoiskAPI: kp.URL,
		Trackers: Trackers{RutorMirrors: []string{dead}, RutorDownload: dead, RutrackerMirrors: []string{rt.Forum.URL},
			RutrackerAPI: rt.API.URL, RutrackerFeed: rt.Feed.URL, NoEdge: true, Rate: 1000}})
	base := "http://" + a.API.Addr() + "/api/v1"
	var tree []catalog.TreeNode
	waitUntil(t, "дерево разделов Rutracker", func() bool {
		getJSON(t, base+"/sources/rutracker/categories", &tree)
		return slices.ContainsFunc(tree, func(n catalog.TreeNode) bool { return n.ID == "2076" && n.ParentID != "" })
	})
	code, body := putJSON(t, base+"/settings", map[string]any{"catalog": map[string]any{"sections": map[string]any{"rutracker": []string{"99999999"}}}})
	if code != http.StatusBadRequest || !strings.Contains(body, "99999999") {
		t.Fatalf("раздела нет в дереве: %d %s", code, body)
	}
	code, body = putJSON(t, base+"/settings", map[string]any{"catalog": map[string]any{"sections": map[string]any{"rutracker": []string{"46+"}, "rutor": []string{"12"}}}})
	if code != 200 {
		t.Fatalf("разделы: %d %s", code, body)
	}
	if v, _, _ := a.DB.Setting(context.Background(), "catalog.categories"); v != "rutracker:46+,rutor:12" {
		t.Fatalf("в базе: %q", v)
	}
}

// Экран раздачи через API: описание со страницы, ссылка «На трекере», список серий из заранее
// скачанного .torrent Rutor — ещё до «Скачать» (спека этапа 7, раздел 5.4).
// rutorApp — сервер с каталогом Rutor на фейковом трекере: постер со страницы раздачи 1077013 — по
// http через фейковый прокси (тесты не ходят на настоящий хостинг картинок).
func rutorApp(t *testing.T) *App {
	t.Helper()
	rutor := rutortest.NewServer(t)
	// Постер со страницы — по http через фейковый прокси: тест не должен ходить на настоящий хостинг.
	topic := bytes.ReplaceAll(rutortest.Page(t, "torrent_1077013.html"), []byte("https://"), []byte("http://"))
	rutor.Override = func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/torrent/1077013" {
			return false
		}
		w.Header().Set("Content-Type", "text/html; charset=UTF-8")
		w.Write(topic)
		return true
	}
	var pic bytes.Buffer
	png.Encode(&pic, image.NewRGBA(image.Rect(0, 0, 1, 1)))
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Host, "127.0.0.1") {
			w.Write(pic.Bytes())
			return
		}
		out, _ := http.NewRequest(r.Method, r.URL.String(), nil)
		out.Header = r.Header.Clone()
		resp, err := http.DefaultTransport.RoundTrip(out)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		for k, v := range resp.Header {
			w.Header()[k] = v
		}
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
	}))
	t.Cleanup(proxy.Close)
	kp := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(kp.Close)
	dead := "http://" + closedAddr(t)
	a := startAppWith(t, Options{Home: t.TempDir(), ListenAddr: "127.0.0.1:0", Offline: true, DownloadsDir: t.TempDir(), KinopoiskAPI: kp.URL,
		Settings: map[string]string{"catalog.categories": "rutor:12", "proxy.trackers": proxy.URL},
		Trackers: Trackers{RutorMirrors: []string{rutor.Mirror.URL}, RutorDownload: rutor.Download.URL,
			RutrackerMirrors: []string{dead}, RutrackerAPI: dead, RutrackerFeed: dead, NoEdge: true, Rate: 1000}})
	return a
}

// rutorRelease — номер раздачи 1077013 (с .torrent) в каталоге, когда её страница догрузилась.
func rutorRelease(t *testing.T, a *App) int64 {
	t.Helper()
	var id int64
	waitUntil(t, "раздача 1077013 в каталоге", func() bool {
		es, _, err := a.Catalog.List(context.Background(), catalog.ListOptions{Tracker: "rutor", Limit: 200})
		if err != nil {
			t.Fatal(err)
		}
		if i := slices.IndexFunc(es, func(e catalog.Entry) bool { return e.TopicID == "1077013" }); i >= 0 {
			id = es[i].ID
		}
		return id != 0
	})
	waitUntil(t, "страница раздачи и .torrent", func() bool {
		r, err := a.Catalog.Release(context.Background(), id)
		return err == nil && !r.DetailsPending && len(r.Torrent) > 0
	})
	return id
}

func TestReleaseCardThroughAPI(t *testing.T) {
	a := rutorApp(t)
	base := "http://" + a.API.Addr() + "/api/v1"
	id := rutorRelease(t, a)
	var rel struct {
		catalog.ReleaseView
		Files []torrents.FileInfo `json:"files"`
	}
	waitUntil(t, "страница раздачи и .torrent", func() bool {
		getJSON(t, fmt.Sprintf("%s/releases/%d", base, id), &rel)
		return !rel.DetailsPending && len(rel.Files) > 0
	})
	if rel.Description == "" || !strings.HasSuffix(rel.TrackerURL, "/torrent/1077013") || rel.Name != "Динозавры" || rel.Hash == "" ||
		rel.Format != "MKV" {
		t.Fatalf("раздача: %+v", rel.ReleaseView)
	}
	// Формат в каталоге — по файлам .torrent, сохранён вместе со страницей (спека этапа 7, раздел 10.2).
	es, _, err := a.Catalog.List(context.Background(), catalog.ListOptions{Tracker: "rutor", Limit: 200})
	if i := slices.IndexFunc(es, func(e catalog.Entry) bool { return e.ID == id }); err != nil || i < 0 || es[i].Format != "MKV" {
		t.Fatalf("формат в каталоге: %v %+v", err, es)
	}
	resp, err := http.Get(base + "/releases/999999")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("нет раздачи: %d", resp.StatusCode)
	}
}

// «Скачать» → очередь → «Смотреть» через API: раздача Rutor открывается из заранее скачанного
// .torrent, её видеофайлы хранятся, первый — в фокусе; «Смотреть» переносит фокус (спека этапа 7,
// раздел 5.5).
// «Скачать» с серии через API (замечание № 3 этапа 11b): {from: N} — очередь всех серий с N; file и from
// вместе — 400.
func TestDownloadFromRoute(t *testing.T) {
	a := rutorApp(t)
	base := "http://" + a.API.Addr() + "/api/v1"
	id := rutorRelease(t, a)
	var opened struct{ Hash string }
	postJSON(t, fmt.Sprintf("%s/releases/%d/download", base, id), struct{}{}, &opened)
	var st torrents.TorrentStatus
	waitUntil(t, "раздача готова", func() bool {
		getJSON(t, base+"/torrents/"+opened.Hash, &st)
		return st.State == torrents.StateReady && len(st.Files) > 1
	})
	last := st.Files[len(st.Files)-1].Index
	postJSON(t, fmt.Sprintf("%s/releases/%d/download", base, id), map[string]int{"from": last}, &opened)
	getJSON(t, base+"/torrents/"+opened.Hash, &st)
	if st.Focus != last {
		t.Fatalf("«Скачать» с последней серии: фокус %d", st.Focus)
	}
	resp, err := http.Post(fmt.Sprintf("%s/releases/%d/download", base, id), "application/json", strings.NewReader(`{"file":0,"from":1}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("file и from вместе: %d", resp.StatusCode)
	}
}

func TestDownloadAndWatchThroughAPI(t *testing.T) {
	a := rutorApp(t)
	base := "http://" + a.API.Addr() + "/api/v1"
	id := rutorRelease(t, a)
	var opened struct{ Hash string }
	postJSON(t, fmt.Sprintf("%s/releases/%d/download", base, id), struct{}{}, &opened)
	var st torrents.TorrentStatus
	waitUntil(t, "раздача готова", func() bool {
		getJSON(t, base+"/torrents/"+opened.Hash, &st)
		return st.State == torrents.StateReady && len(st.Files) > 0
	})
	if st.Focus < 0 || !st.Files[0].Stored || st.Files[0].Readiness != torrents.ReadyWait {
		t.Fatalf("после «Скачать»: фокус %d, %+v", st.Focus, st.Files[0])
	}
	last := st.Files[len(st.Files)-1].Index
	var w struct {
		Play      torrents.Play `json:"play"`
		LaunchURL *string       `json:"launchUrl"`
	}
	postJSON(t, fmt.Sprintf("%s/torrents/%s/files/%d/watch", base, opened.Hash, last), struct{}{}, &w)
	getJSON(t, base+"/torrents/"+opened.Hash, &st)
	if st.Focus != last || !strings.Contains(w.Play.URL, "/stream/"+opened.Hash) || w.LaunchURL == nil {
		t.Fatalf("«Смотреть»: фокус %d, %+v", st.Focus, w)
	}
	var dl struct {
		Items []struct {
			State   torrents.DownloadState `json:"state"`
			Release *catalog.ReleaseRef    `json:"release"`
		} `json:"items"`
		FreeBytes int64 `json:"freeBytes"`
	}
	getJSON(t, base+"/downloads", &dl)
	if len(dl.Items) != len(st.Files) || dl.Items[0].State != torrents.DownloadDownloading || dl.Items[0].Release == nil ||
		!strings.HasPrefix(dl.Items[0].Release.Title, "Динозавры") || dl.FreeBytes <= 0 {
		t.Fatalf("загрузки: %+v", dl)
	}
	if code, body := putJSON(t, base+"/settings", map[string]any{"storage": map[string]any{"uploadLimitMBps": 0}}); code != 200 {
		t.Fatalf("раздача 0: %d %s", code, body)
	}
	if got := a.Torrents.UploadLimit(); got != torrents.NoUpload {
		t.Fatalf("раздача 0 не применилась: %v", got)
	}
	resp, err := http.Post(base+"/releases/999999/download", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("нет раздачи: %d", resp.StatusCode)
	}
}

// Трекер по его проблемам: не ответил — «не отвечает», раздел, форум, разбор или вход Rutracker —
// «частично»; «не отвечает» хуже (спека этапа 7, раздел 5.7).
func TestTrackerStateFromProblems(t *testing.T) {
	ps := []store.Problem{
		{ID: "rutracker.login", Text: "Rutracker: капча — вход не выполнен"},
		{ID: "catalog.rutor:12", Text: "Rutor: в разделе «Научно-популярные» пришло 3 раздачи вместо 100"},
		{ID: "catalog.rutor", Text: "Rutor недоступен"},
		{ID: "proxy.invalid", Text: "Прокси не работает"},
	}
	if st := trackerOf(ps, "rutracker"); st.State != "warn" || !strings.Contains(st.Text, "капча") {
		t.Fatalf("Rutracker: %+v", st)
	}
	if st := trackerOf(ps, "rutor"); st.State != "down" || st.Text != "Rutor недоступен" {
		t.Fatalf("Rutor: %+v", st)
	}
	if st := trackerOf(ps[3:], "rutor"); st.State != "ok" {
		t.Fatalf("без проблем трекера: %+v", st)
	}
	// Адрес не введён — «выключен», это важнее любых прежних проблем трекера (этап 11a).
	off := append(ps, store.Problem{ID: "catalog.rutor.address", Text: "Укажите адрес Rutor в настройках"})
	if st := trackerOf(off, "rutor"); st.State != "off" || st.Text != "Укажите адрес Rutor в настройках" {
		t.Fatalf("без адреса: %+v", st)
	}
}

// «Состояние» через API: с этого ПК, оба трекера (у Rutracker — вход), ключ Кинопоиска, место в
// папке загрузок, потоки.
func TestStatusThroughAPI(t *testing.T) {
	a := startApp(t)
	var st struct {
		Local    bool `json:"local"`
		Trackers map[string]struct {
			State string               `json:"state"`
			Login *rutracker.LoginInfo `json:"login"`
		} `json:"trackers"`
		Kinopoisk struct {
			KeySet  bool `json:"keySet"`
			Keyless *struct {
				PausedUntil *time.Time `json:"pausedUntil"`
				Today       int        `json:"today"`
			} `json:"keyless"`
		} `json:"kinopoisk"`
		Disk    torrents.DiskInfo `json:"disk"`
		Streams struct {
			Count int `json:"count"`
		} `json:"streams"`
	}
	getJSON(t, "http://"+a.API.Addr()+"/api/v1/status", &st)
	if rt, ok := st.Trackers["rutracker"]; !st.Local || !ok || rt.Login == nil || rt.Login.State != rutracker.LoginNone || st.Trackers["rutor"].State == "" {
		t.Fatalf("трекеры: %+v", st.Trackers)
	}
	if st.Kinopoisk.Keyless == nil || st.Kinopoisk.Keyless.PausedUntil != nil {
		t.Fatalf("Кинопоиск без токена: %+v", st.Kinopoisk.Keyless)
	}
	if st.Kinopoisk.KeySet || st.Disk.FreeBytes <= 0 || st.Disk.TotalBytes < st.Disk.FreeBytes || st.Disk.MinFreeBytes != 20<<30 ||
		st.Streams.Count != 0 {
		t.Fatalf("состояние: %+v", st)
	}
}

// Каналы работают вместе с остальными модулями (спека этапа 8): маршруты отвечают, настройки скрытия и
// часового пояса действуют без перезапуска, «Состояние» показывает модуль; выключенный модуль —
// 503 только у его маршрутов, остальные работают.
func TestIPTVWithOtherModules(t *testing.T) {
	a := startApp(t)
	base := "http://" + a.API.Addr()
	var ch struct {
		Channels []any `json:"channels"`
	}
	getJSON(t, base+"/api/v1/channels", &ch)
	if ch.Channels == nil {
		t.Fatal("каналы — null вместо []")
	}
	var pls struct {
		Items []any `json:"items"`
	}
	getJSON(t, base+"/api/v1/iptv/playlists", &pls)
	code, body := putJSON(t, base+"/api/v1/settings", map[string]any{"iptv": map[string]any{"hiddenCategories": []string{"sports"}, "utcOffset": 3}})
	if code != http.StatusOK || !strings.Contains(body, `"hiddenCategories":["sports"]`) {
		t.Fatalf("настройки каналов: %d %s", code, body)
	}
	if l := a.IPTV.Lineup(); l.LocalShift != 0 {
		t.Errorf("пояс каналов UTC+3 не применился: сдвиг %d", l.LocalShift)
	}
	var st struct {
		IPTV map[string]any `json:"iptv"`
	}
	getJSON(t, base+"/api/v1/status", &st)
	if _, ok := st.IPTV["channels"]; !ok {
		t.Errorf("в «Состоянии» нет каналов: %v", st.IPTV)
	}

	// Модуль выключен настройкой разработчика: маршруты каналов — 503, загрузки работают.
	a2home := t.TempDir()
	paths := config.NewPaths(a2home)
	if err := paths.Ensure(); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(context.Background(), paths.DB)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetSetting(context.Background(), "modules.iptv.enabled", "false"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	a2 := startAppWith(t, Options{Home: a2home, ListenAddr: "127.0.0.1:0", Offline: true, DownloadsDir: t.TempDir()})
	resp, err := http.Get("http://" + a2.API.Addr() + "/api/v1/channels")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable || !strings.Contains(string(b), "Каналы") {
		t.Errorf("выключенный модуль: %d %s", resp.StatusCode, b)
	}
	getJSON(t, "http://"+a2.API.Addr()+"/api/v1/downloads", &map[string]any{})
}

// История просмотров (спека этапа 8, раздел 7.4): место от своего плеера, отметка «просмотрено»,
// история устройства, убрать раздачу из истории.
func TestHistoryRoutes(t *testing.T) {
	a := startApp(t)
	base := "http://" + a.API.Addr() + "/api/v1/history"
	const h = "aaaa000000000000000000000000000000000001"
	if code, body := putJSON(t, base+"/"+h+"/2", map[string]any{"positionSec": 1800, "durationSec": 2400}); code != http.StatusNoContent {
		t.Fatalf("место: %d %s", code, body)
	}
	if code, _ := putJSON(t, base+"/"+h+"/3", map[string]any{"watched": true}); code != http.StatusNoContent {
		t.Fatalf("отметка: %d", code)
	}
	if code, _ := putJSON(t, base+"/"+h+"/3", map[string]any{"positionSec": 10, "durationSec": 5}); code != http.StatusBadRequest {
		t.Errorf("место больше длительности: %d", code)
	}
	if code, _ := putJSON(t, base+"/не-хэш/3", map[string]any{"watched": true}); code != http.StatusBadRequest {
		t.Errorf("неверный хэш: %d", code)
	}
	var files struct {
		Files []struct {
			Index       int     `json:"index"`
			Fraction    float64 `json:"fraction"`
			PositionSec float64 `json:"positionSec"`
			Watched     bool    `json:"watched"`
		} `json:"files"`
	}
	getJSON(t, base+"/"+h, &files)
	if len(files.Files) != 2 || files.Files[0].PositionSec != 1800 || files.Files[0].Fraction != 0.75 || !files.Files[1].Watched {
		t.Fatalf("файлы: %+v", files)
	}
	var list struct {
		Items []struct {
			Hash    string `json:"hash"`
			Watched int    `json:"watched"`
			Files   int    `json:"files"`
			Release any    `json:"release"`
		} `json:"items"`
	}
	getJSON(t, base, &list)
	if len(list.Items) != 1 || list.Items[0].Hash != h || list.Items[0].Watched != 1 || list.Items[0].Files != 2 || list.Items[0].Release != nil {
		t.Fatalf("история: %+v", list)
	}
	req, _ := http.NewRequest(http.MethodDelete, base+"/"+h, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusNoContent {
		t.Fatalf("удаление: %v %v", resp, err)
	}
	resp.Body.Close()
	getJSON(t, base, &list)
	if len(list.Items) != 0 {
		t.Errorf("после удаления: %+v", list)
	}
}

// Медиатека вместе с загрузками и историей (спека этапа 9): скачанная раздача — карточка с данными
// раздачи; файл из папки — в истории по «раздаче» lib-<единица>; скрытая категория в «Истории» не
// видна.
func TestLibraryThroughAPI(t *testing.T) {
	a := rutorApp(t)
	base := "http://" + a.API.Addr() + "/api/v1"
	id := rutorRelease(t, a)
	var opened struct{ Hash string }
	postJSON(t, fmt.Sprintf("%s/releases/%d/download", base, id), struct{}{}, &opened)
	type list struct {
		Cards []struct {
			Key   string `json:"key"`
			Title string `json:"title"`
		} `json:"cards"`
	}
	var l list
	waitUntil(t, "скачанная раздача в медиатеке", func() bool {
		postJSON(t, base+"/library/scan", struct{}{}, nil)
		getJSON(t, base+"/library", &l)
		return len(l.Cards) == 1
	})
	if !strings.HasPrefix(l.Cards[0].Title, "Динозавры") {
		t.Errorf("карточка скачанного: %+v", l.Cards)
	}
	dir := t.TempDir()
	film := filepath.Join(dir, "Домашнее.mkv")
	if err := os.WriteFile(film, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	os.Chtimes(film, old, old)
	var created struct{ ID int64 }
	b, _ := json.Marshal(map[string]any{"name": "Дом", "layout": "films", "hidden": true, "folders": []string{dir}})
	resp, err := http.Post(base+"/library/categories", "application/json", bytes.NewReader(b))
	if err != nil || resp.StatusCode != http.StatusCreated {
		t.Fatalf("категория: %v %v", resp, err)
	}
	json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()
	var unit, file int64
	waitUntil(t, "файл из папки в медиатеке", func() bool {
		a.DB.R.QueryRow(`SELECT u.id, f.id FROM lib_units u JOIN lib_files f ON f.unit = u.id WHERE u.source = 'folder'`).Scan(&unit, &file)
		return unit > 0
	})
	h := fmt.Sprintf("lib-%d", unit)
	if code, body := putJSON(t, fmt.Sprintf("%s/history/%s/%d", base, h, file), map[string]any{"watched": true}); code != http.StatusNoContent {
		t.Fatalf("отметка файла медиатеки: %d %s", code, body)
	}
	type hist struct {
		Items []struct {
			Hash    string `json:"hash"`
			File    string `json:"file"`
			Library *struct {
				Key   string `json:"key"`
				Title string `json:"title"`
			} `json:"library"`
		} `json:"items"`
	}
	var hs hist
	getJSON(t, base+"/history", &hs)
	for _, it := range hs.Items {
		if it.Hash == h {
			t.Errorf("скрытая категория в «Истории»: %+v", it)
		}
	}
	if code, body := putJSON(t, fmt.Sprintf("%s/library/categories/%d/device", base, created.ID), struct{}{}); code != http.StatusNoContent {
		t.Fatalf("показывать на устройстве: %d %s", code, body)
	}
	getJSON(t, base+"/history", &hs)
	found := false
	for _, it := range hs.Items {
		if it.Hash == h {
			found = it.Library != nil && it.Library.Title == "Домашнее" && it.File == "Домашнее.mkv"
		}
	}
	if !found {
		t.Errorf("файл медиатеки в «Истории»: %+v", hs.Items)
	}
}
