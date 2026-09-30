package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"kinodom/internal/config"
	"kinodom/internal/source/t1/t1test"
	"kinodom/internal/source/t2/t2test"
	"kinodom/internal/store"
)

// Адресов трекеров нет (этап 11a, чистая установка): сервер стартует, трекеры выключены с понятной
// строкой, в сеть к ним ничего не идёт; поиск отвечает пусто и без ошибки. Адрес t1 ввели в
// пульте — каталог t1 работает без перезапуска.
func TestTrackersOffUntilAddressIsSet(t *testing.T) {
	t1 := t1test.NewServer(t)
	home := t.TempDir()
	db, err := store.Open(context.Background(), config.NewPaths(home).DB)
	if err != nil {
		t.Fatal(err)
	}
	db.SetSetting(context.Background(), "catalog.categories", "t1:12")
	db.Close()
	kp := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(kp.Close)
	a := startAppWith(t, Options{Home: home, ListenAddr: "127.0.0.1:0", Offline: true, DownloadsDir: t.TempDir(), KinopoiskAPI: kp.URL,
		Trackers: Trackers{NoEdge: true, Rate: 1000}})
	base := "http://" + a.API.Addr()
	type status struct {
		Trackers map[string]trackerStatus `json:"trackers"`
	}
	var st status
	waitUntil(t, "трекеры выключены", func() bool {
		getJSON(t, base+"/api/v1/status", &st)
		return st.Trackers["t1"].State == "off" && st.Trackers["t2"].State == "off"
	})
	if st.Trackers["t1"].Text != "Укажите адрес t1 в настройках" || st.Trackers["t2"].Text != "Укажите адрес t2 в настройках" {
		t.Fatalf("тексты: %+v", st.Trackers)
	}
	var found struct {
		Results  []any             `json:"results"`
		Complete bool              `json:"complete"`
		Trackers map[string]string `json:"trackers"`
	}
	waitUntil(t, "поиск без адресов закончен", func() bool {
		getJSON(t, base+"/api/v1/search?q=матрица", &found)
		return found.Complete
	})
	if len(found.Results) != 0 || len(found.Trackers) != 0 {
		t.Fatalf("поиск без адресов: %+v", found)
	}
	if len(t1.Paths()) != 0 {
		t.Fatalf("к трекеру ходили без адреса: %v", t1.Paths())
	}

	code, body := putJSON(t, base+"/api/v1/settings", map[string]any{
		"t1": map[string]any{"address": t1.Mirror.URL, "downloadAddress": t1.Download.URL}})
	if code != http.StatusOK {
		t.Fatalf("адрес t1: %d %s", code, body)
	}
	waitUntil(t, "каталог t1 после ввода адреса", func() bool {
		getJSON(t, base+"/api/v1/status", &st)
		return st.Trackers["t1"].State == "ok" && len(t1.Paths()) > 0
	})
	if st.Trackers["t2"].State != "off" {
		t.Fatalf("t2 без адреса: %+v", st.Trackers["t2"])
	}
	code, body = putJSON(t, base+"/api/v1/settings", map[string]any{"t1": map[string]any{"address": "абв"}})
	if code != http.StatusBadRequest {
		t.Fatalf("мусор в адресе: %d %s", code, body)
	}
}

// Версия сервера — в «Состоянии»: её показывает kinodom check (этап 11a).
func TestStatusHasVersion(t *testing.T) {
	a := startAppWith(t, Options{Home: t.TempDir(), ListenAddr: "127.0.0.1:0", Offline: true, DownloadsDir: t.TempDir(), Version: "0.11.0-test"})
	var st struct {
		Version string `json:"version"`
	}
	getJSON(t, "http://"+a.API.Addr()+"/api/v1/status", &st)
	if st.Version != "0.11.0-test" {
		t.Fatalf("версия %q", st.Version)
	}
}

// Вход t2 переживает перезапуск приложения (хвост 5c): сессия — в базе (tracker_state).
func Testt2SessionSurvivesRestart(t *testing.T) {
	rt := t2test.NewServer(t)
	rt.Login, rt.Password, rt.TopicNeedsLogin = "user", "pass", true
	kp := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(kp.Close)
	home := t.TempDir()
	o := Options{Home: home, ListenAddr: "127.0.0.1:0", Offline: true, DownloadsDir: t.TempDir(), KinopoiskAPI: kp.URL,
		Settings: map[string]string{"t2.login": "user", "t2.password": "pass"},
		Trackers: Trackers{t2Mirrors: []string{rt.Forum.URL}, t2API: rt.API.URL, t2Feed: rt.Feed.URL,
			t1Mirrors: []string{"http://" + closedAddr(t)}, NoEdge: true, Rate: 1000}}
	ctx, cancel := context.WithCancel(context.Background())
	a, err := New(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.t2.Details(ctx, "6914565"); err != nil {
		t.Fatal(err)
	}
	cancel()
	a.Close()
	if rt.Logins() != 1 {
		t.Fatalf("входов %d", rt.Logins())
	}
	a2 := startAppRaw(t, o)
	if _, err := a2.t2.Details(context.Background(), "6914565"); err != nil {
		t.Fatal(err)
	}
	if rt.Logins() != 1 {
		t.Fatalf("после перезапуска входов %d — сессия не сохранилась", rt.Logins())
	}
}
