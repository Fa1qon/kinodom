package appdist

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

type muxRouter struct{ *http.ServeMux }

func (m muxRouter) Handle(pattern, _ string, h http.Handler) { m.ServeMux.Handle(pattern, h) }

func serve(dir string) *http.ServeMux {
	mux := http.NewServeMux()
	New(dir).Register(muxRouter{mux})
	return mux
}

func get(mux *http.ServeMux, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
	return rec
}

// APK рядом с программой (спека этапа 13, раздел 5.3): /app и /app/kinodom.apk отдают файл с именем версии,
// /api/v1/app — версию, номер сборки, адрес и размер.
func TestAppDistServes(t *testing.T) {
	dir := t.TempDir()
	apk := []byte("PK\x03\x04 содержимое apk")
	os.WriteFile(filepath.Join(dir, "kinodom.apk"), apk, 0o644)
	os.WriteFile(filepath.Join(dir, "kinodom.apk.json"), []byte(`{"version":"0.11.0-abc1234","versionCode":512}`), 0o644)
	mux := serve(dir)
	for _, path := range []string{"/app", "/app/kinodom.apk"} {
		rec := get(mux, path)
		if rec.Code != 200 || rec.Body.String() != string(apk) {
			t.Fatalf("%s: %d, %d байт", path, rec.Code, rec.Body.Len())
		}
		if ct := rec.Header().Get("Content-Type"); ct != "application/vnd.android.package-archive" {
			t.Fatalf("%s: тип %q", path, ct)
		}
		if cd := rec.Header().Get("Content-Disposition"); cd != `attachment; filename="Kinodom-0.11.0-abc1234.apk"` {
			t.Fatalf("%s: имя %q", path, cd)
		}
	}
	rec := get(mux, "/api/v1/app")
	var v struct {
		Version     string `json:"version"`
		VersionCode int    `json:"versionCode"`
		URL         string `json:"url"`
		Size        int64  `json:"size"`
	}
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &v) != nil {
		t.Fatalf("сведения: %d %s", rec.Code, rec.Body.String())
	}
	if v.Version != "0.11.0-abc1234" || v.VersionCode != 512 || v.URL != "/app/kinodom.apk" || v.Size != int64(len(apk)) {
		t.Fatalf("сведения: %+v", v)
	}
}

// Сборка без Android (APK нет) или испорченные сведения — 404: приложение не предлагает обновление, а
// «Состояние» не показывает строку.
func TestAppDistMissing(t *testing.T) {
	cases := map[string]func(dir string){
		"нет ничего": func(string) {},
		"нет json":   func(d string) { os.WriteFile(filepath.Join(d, "kinodom.apk"), []byte("PK"), 0o644) },
		"нет apk": func(d string) {
			os.WriteFile(filepath.Join(d, "kinodom.apk.json"), []byte(`{"version":"1","versionCode":1}`), 0o644)
		},
		"битый json": func(d string) {
			os.WriteFile(filepath.Join(d, "kinodom.apk"), []byte("PK"), 0o644)
			os.WriteFile(filepath.Join(d, "kinodom.apk.json"), []byte(`{`), 0o644)
		},
		"без номера": func(d string) {
			os.WriteFile(filepath.Join(d, "kinodom.apk"), []byte("PK"), 0o644)
			os.WriteFile(filepath.Join(d, "kinodom.apk.json"), []byte(`{"version":"1"}`), 0o644)
		},
	}
	for name, prepare := range cases {
		dir := t.TempDir()
		prepare(dir)
		mux := serve(dir)
		for _, path := range []string{"/app", "/app/kinodom.apk", "/api/v1/app"} {
			if code := get(mux, path).Code; code != http.StatusNotFound {
				t.Errorf("%s, %s: %d", name, path, code)
			}
		}
	}
}
