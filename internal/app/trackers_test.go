package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"kinodom/internal/config"
	"kinodom/internal/source/rutor/rutortest"
	"kinodom/internal/store"
)

// Адресов трекеров нет (этап 11a, чистая установка): сервер стартует, трекеры выключены с понятной
// строкой, в сеть к ним ничего не идёт; поиск отвечает пусто и без ошибки. Адрес Rutor ввели в
// пульте — каталог Rutor работает без перезапуска.
func TestTrackersOffUntilAddressIsSet(t *testing.T) {
	rutor := rutortest.NewServer(t)
	home := t.TempDir()
	db, err := store.Open(context.Background(), config.NewPaths(home).DB)
	if err != nil {
		t.Fatal(err)
	}
	db.SetSetting(context.Background(), "catalog.categories", "rutor:12")
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
		return st.Trackers["rutor"].State == "off" && st.Trackers["rutracker"].State == "off"
	})
	if st.Trackers["rutor"].Text != "Укажите адрес Rutor в настройках" || st.Trackers["rutracker"].Text != "Укажите адрес Rutracker в настройках" {
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
	if len(rutor.Paths()) != 0 {
		t.Fatalf("к трекеру ходили без адреса: %v", rutor.Paths())
	}

	code, body := putJSON(t, base+"/api/v1/settings", map[string]any{
		"rutor": map[string]any{"address": rutor.Mirror.URL, "downloadAddress": rutor.Download.URL}})
	if code != http.StatusOK {
		t.Fatalf("адрес Rutor: %d %s", code, body)
	}
	waitUntil(t, "каталог Rutor после ввода адреса", func() bool {
		getJSON(t, base+"/api/v1/status", &st)
		return st.Trackers["rutor"].State == "ok" && len(rutor.Paths()) > 0
	})
	if st.Trackers["rutracker"].State != "off" {
		t.Fatalf("Rutracker без адреса: %+v", st.Trackers["rutracker"])
	}
	code, body = putJSON(t, base+"/api/v1/settings", map[string]any{"rutor": map[string]any{"address": "абв"}})
	if code != http.StatusBadRequest {
		t.Fatalf("мусор в адресе: %d %s", code, body)
	}
}
