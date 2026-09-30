package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Безопасность одним набором (спека этапа 11a, раздел 9; спека этапа 7, раздел 10.1) — по всем
// маршрутам, в том числе новым: мастер, обзор папок, «Разрешить доступ».
func TestSecurity(t *testing.T) {
	a := startApp(t)
	h := a.API.Handler()
	do := func(method, path, host, remote, contentType, body string) int {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Host, r.RemoteAddr = host, remote
		if contentType != "" {
			r.Header.Set("Content-Type", contentType)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec.Code
	}
	const (
		pc     = "127.0.0.1:50000"
		lan    = "192.168.0.7:50000"
		street = "8.8.8.8:50000"
		json   = "application/json"
	)
	type route struct{ method, path, body string }
	reads := []route{
		{"GET", "/", ""}, {"GET", "/api/v1/status", ""}, {"GET", "/api/v1/settings", ""},
		{"GET", "/api/v1/fs/dirs", ""}, {"GET", "/api/v1/setup/addresses", ""}, {"GET", "/api/v1/library/access?path=C%3A%5C", ""},
	}
	changes := []route{
		{"PUT", "/api/v1/settings", `{"player":"vlc"}`},
		{"POST", "/api/v1/setup/check", `{"tracker":"rutor"}`},
		{"POST", "/api/v1/library/scan", `{}`},
		{"POST", "/api/v1/iptv/playlists", `{"url":"http://127.0.0.1:1/x.m3u"}`},
	}

	// DNS rebinding: чужое имя в Host — отказ на любом маршруте, даже с этого ПК.
	for _, r := range append(reads, changes...) {
		if code := do(r.method, r.path, "evil.example:8090", pc, json, r.body); code != http.StatusMisdirectedRequest {
			t.Errorf("чужой Host, %s %s: код %d", r.method, r.path, code)
		}
	}
	// HTML-форма чужой страницы (не JSON) ничего не меняет.
	for _, r := range changes {
		if code := do(r.method, r.path, "localhost", pc, "application/x-www-form-urlencoded", "player=vlc"); code != http.StatusUnsupportedMediaType {
			t.Errorf("форма, %s %s: код %d", r.method, r.path, code)
		}
	}
	// Изменения, мастер и обзор папок — только из домашней сети. Обход медиатеки (library/scan) —
	// не изменение: его просит сам экран медиатеки при открытии с любого устройства (этап 9).
	for _, r := range append(changes, route{"GET", "/api/v1/fs/dirs", ""}, route{"GET", "/api/v1/setup/addresses", ""}) {
		if r.path == "/api/v1/library/scan" {
			continue
		}
		if code := do(r.method, r.path, "localhost", street, json, r.body); code != http.StatusForbidden {
			t.Errorf("не из дома, %s %s: код %d", r.method, r.path, code)
		}
	}
	// «Чья это папка» — только с этого ПК: с телефона в домашней сети — нет.
	if code := do("GET", "/api/v1/library/access?path=C%3A%5C", "localhost", lan, "", ""); code != http.StatusForbidden {
		t.Errorf("library/access из домашней сети не с ПК: код %d", code)
	}
	if code := do("GET", "/api/v1/library/access?path=C%3A%5C", "localhost", pc, "", ""); code != http.StatusOK {
		t.Errorf("library/access с ПК: код %d", code)
	}
}
