package app

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"kinodom/internal/source/rutor/rutortest"
	"kinodom/internal/source/rutracker/rutrackertest"
)

type checkResult struct {
	OK        bool   `json:"ok"`
	Text      string `json:"text"`
	DailyLeft *int   `json:"dailyLeft"`
}

func setupCheck(t *testing.T, base string, body any) checkResult {
	t.Helper()
	b, _ := json.Marshal(body)
	resp, err := http.Post(base+"/api/v1/setup/check", "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("проверка %v: код %d %s", body, resp.StatusCode, raw)
	}
	var r checkResult
	json.NewDecoder(resp.Body).Decode(&r)
	return r
}

// «Проверить» у трекеров в мастере (спека этапа 11a, раздел 7): итог одной строкой.
func TestSetupCheckTrackers(t *testing.T) {
	a := startApp(t)
	base := "http://" + a.API.Addr()
	if r := setupCheck(t, base, map[string]string{"tracker": "rutor"}); r.OK || r.Text != "Укажите адрес Rutor" {
		t.Fatalf("без адреса: %+v", r)
	}
	rutor := rutortest.NewServer(t)
	put := func(v map[string]any) {
		t.Helper()
		if code, body := putJSON(t, base+"/api/v1/settings", v); code != http.StatusOK {
			t.Fatalf("настройки: %d %s", code, body)
		}
	}
	put(map[string]any{"rutor": map[string]any{"address": rutor.Mirror.URL, "downloadAddress": rutor.Download.URL}})
	if r := setupCheck(t, base, map[string]string{"tracker": "rutor"}); !r.OK || r.Text != "Отвечает" {
		t.Fatalf("Rutor: %+v", r)
	}
	shop := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=UTF-8")
		io.WriteString(w, "<html><title>Магазин</title></html>")
	}))
	t.Cleanup(shop.Close)
	put(map[string]any{"rutor": map[string]any{"address": shop.URL}})
	if r := setupCheck(t, base, map[string]string{"tracker": "rutor"}); r.OK || r.Text != "Адрес не похож на сайт трекера" {
		t.Fatalf("чужой сайт: %+v", r)
	}
	put(map[string]any{"rutor": map[string]any{"address": "http://" + closedAddr(t)}})
	if r := setupCheck(t, base, map[string]string{"tracker": "rutor"}); r.OK || r.Text != "Сайт не отвечает — нужен прокси?" {
		t.Fatalf("не отвечает: %+v", r)
	}

	rt := rutrackertest.NewServer(t)
	rt.Login, rt.Password = "user", "right"
	put(map[string]any{"rutracker": map[string]any{"address": rt.Forum.URL, "apiAddress": rt.API.URL, "feedAddress": rt.Feed.URL}})
	if r := setupCheck(t, base, map[string]string{"tracker": "rutracker"}); !r.OK || r.Text != "Отвечает" {
		t.Fatalf("Rutracker без логина: %+v", r)
	}
	put(map[string]any{"rutracker": map[string]any{"login": "user", "password": "wrong"}})
	if r := setupCheck(t, base, map[string]string{"tracker": "rutracker"}); r.OK || r.Text != "Неверный логин или пароль" {
		t.Fatalf("неверный пароль: %+v", r)
	}
	put(map[string]any{"rutracker": map[string]any{"password": "right"}})
	if r := setupCheck(t, base, map[string]string{"tracker": "rutracker"}); !r.OK || r.Text != "Отвечает, вход выполнен" {
		t.Fatalf("вход: %+v", r)
	}
}

// Ключ Кинопоиска в мастере: подходит — сколько запросов осталось на сутки; ключ — не в журнал.
func TestSetupCheckKinopoisk(t *testing.T) {
	kp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/api_keys/SECRET-GOOD-KEY" {
			io.WriteString(w, `{"totalQuota":{"value":-1,"used":0},"dailyQuota":{"value":500,"used":20},"accountType":"FREE"}`)
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(kp.Close)
	home := t.TempDir()
	a := startAppWith(t, Options{Home: home, ListenAddr: "127.0.0.1:0", Offline: true, DownloadsDir: t.TempDir(), KinopoiskAPI: kp.URL})
	base := "http://" + a.API.Addr()
	if r := setupCheck(t, base, map[string]bool{"kinopoisk": true}); r.OK || r.Text != "Ключ не указан" {
		t.Fatalf("без ключа: %+v", r)
	}
	putJSON(t, base+"/api/v1/settings", map[string]any{"kinopoisk": map[string]any{"key": "SECRET-GOOD-KEY"}})
	r := setupCheck(t, base, map[string]bool{"kinopoisk": true})
	if !r.OK || r.Text != "Ключ подходит, осталось 480 запросов в сутки" || r.DailyLeft == nil || *r.DailyLeft != 480 {
		t.Fatalf("ключ подходит: %+v", r)
	}
	putJSON(t, base+"/api/v1/settings", map[string]any{"kinopoisk": map[string]any{"key": "SECRET-BAD-KEY"}})
	if r := setupCheck(t, base, map[string]bool{"kinopoisk": true}); r.OK || r.Text != "Ключ не подходит" {
		t.Fatalf("ключ не подходит: %+v", r)
	}
	logs, _ := os.ReadFile(filepath.Join(home, "data", "logs", "kinodom.log"))
	if strings.Contains(string(logs), "SECRET-") {
		t.Fatal("ключ Кинопоиска в журнале")
	}
}

// Обзор папок (спека этапа 11a, раздел 7): диски; подпапки по имени — без скрытых, системных и
// файлов; только локальные диски; только из домашней сети.
func TestFolderBrowser(t *testing.T) {
	a := startApp(t)
	get := func(remote, path string) (int, dirsView) {
		u := "/api/v1/fs/dirs"
		if path != "" {
			u += "?path=" + url.QueryEscape(path)
		}
		req := httptest.NewRequest("GET", u, nil)
		req.Host, req.RemoteAddr = "localhost", remote
		rec := httptest.NewRecorder()
		a.API.Handler().ServeHTTP(rec, req)
		var v dirsView
		json.Unmarshal(rec.Body.Bytes(), &v)
		return rec.Code, v
	}
	const home = "192.168.0.7:50000"
	code, v := get(home, "")
	if code != http.StatusOK || len(v.Drives) == 0 || v.Drives[0].Path != `C:\` || v.Drives[0].Free <= 0 {
		t.Fatalf("диски: %d %+v", code, v)
	}
	root := t.TempDir()
	for _, d := range []string{"b-сериалы", "A-фильмы", "скрытая"} {
		os.Mkdir(filepath.Join(root, d), 0o755)
	}
	os.WriteFile(filepath.Join(root, "файл.mkv"), []byte("x"), 0o644)
	p, _ := syscall.UTF16PtrFromString(filepath.Join(root, "скрытая"))
	syscall.SetFileAttributes(p, syscall.FILE_ATTRIBUTE_HIDDEN)
	code, v = get(home, filepath.Join(root, "A-фильмы", ".."))
	if code != http.StatusOK || v.Path != root || v.Parent != filepath.Dir(root) || strings.Join(v.Dirs, "|") != "A-фильмы|b-сериалы" {
		t.Fatalf("папка: %d %+v", code, v)
	}
	if _, v = get(home, `C:\`); v.Parent != "" {
		t.Fatalf("у корня диска родитель %q", v.Parent)
	}
	for _, bad := range []string{`\\server\share`, `\\?\C:\Windows`, "Movies", filepath.Join(root, "нет"), filepath.Join(root, "файл.mkv")} {
		if code, _ := get(home, bad); code != http.StatusBadRequest {
			t.Errorf("%s: код %d", bad, code)
		}
	}
	if code, _ := get("8.8.8.8:50000", root); code != http.StatusForbidden {
		t.Fatalf("не из дома: %d", code)
	}
}

// Адреса ПК для экрана «Готово» и признак «мастер пройден» в «Состоянии».
func TestSetupAddressesAndDone(t *testing.T) {
	a := startApp(t)
	base := "http://" + a.API.Addr()
	var addrs []string
	getJSON(t, base+"/api/v1/setup/addresses", &addrs)
	for _, u := range addrs {
		if !strings.HasPrefix(u, "http://") || !strings.HasSuffix(u, ":"+strings.Split(a.API.Addr(), ":")[1]) {
			t.Fatalf("адрес %q", u)
		}
	}
	var st struct {
		SetupDone bool `json:"setupDone"`
	}
	getJSON(t, base+"/api/v1/status", &st)
	if st.SetupDone {
		t.Fatal("мастер пройден в чистой базе")
	}
	putJSON(t, base+"/api/v1/settings", map[string]any{"setup": map[string]any{"done": true}})
	getJSON(t, base+"/api/v1/status", &st)
	if !st.SetupDone {
		t.Fatal("мастер не пройден после «Готово»")
	}
}

// Проверки мастера и обзор папок — только из домашней сети.
func TestSetupRoutesHomeOnly(t *testing.T) {
	a := startApp(t)
	for _, r := range []*http.Request{
		httptest.NewRequest("POST", "/api/v1/setup/check", strings.NewReader(`{"tracker":"rutor"}`)),
		httptest.NewRequest("GET", "/api/v1/setup/addresses", nil),
	} {
		r.Header.Set("Content-Type", "application/json")
		r.Host, r.RemoteAddr = "localhost", "8.8.8.8:50000"
		rec := httptest.NewRecorder()
		a.API.Handler().ServeHTTP(rec, r)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s %s: код %d", r.Method, r.URL.Path, rec.Code)
		}
	}
}
