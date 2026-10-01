// Package appdist — сервер раздаёт приложение для Android (спека этапа 13, раздел 5.3): установщик для ПК кладёт
// kinodom.apk и kinodom.apk.json (версия и номер сборки) рядом с программой; первый раз APK качают по адресу
// http://<ПК>:<порт>/app, дальше приложение само спрашивает /api/v1/app и предлагает «Обновить».
package appdist

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"

	"kinodom/internal/httpx"
)

const (
	apkName  = "kinodom.apk"
	infoName = "kinodom.apk.json"
	apkType  = "application/vnd.android.package-archive"
)

// Info — APK на сервере: версия (как у сервера), номер сборки (растёт с каждой сборкой), размер.
type Info struct {
	Version     string `json:"version"`
	VersionCode int    `json:"versionCode"`
	Size        int64  `json:"size"`
}

// Dist — APK в папке программы.
type Dist struct{ dir string }

// Router — то, что пакету нужно от HTTP-сервера; api.Server ему соответствует.
type Router interface {
	Handle(pattern, module string, h http.Handler)
}

func New(dir string) *Dist { return &Dist{dir: dir} }

// Info — сведения об APK; нет файла, нет сведений или они испорчены — false.
func (d *Dist) Info() (Info, bool) {
	b, err := os.ReadFile(filepath.Join(d.dir, infoName))
	if err != nil {
		return Info{}, false
	}
	var in Info
	if json.Unmarshal(b, &in) != nil || in.Version == "" || in.VersionCode <= 0 {
		return Info{}, false
	}
	fi, err := os.Stat(filepath.Join(d.dir, apkName))
	if err != nil || fi.IsDir() {
		return Info{}, false
	}
	in.Size = fi.Size()
	return in, true
}

// Register — GET /app и /app/kinodom.apk (файл), GET /api/v1/app (сведения); APK нет — 404.
func (d *Dist) Register(r Router) {
	file := http.HandlerFunc(d.serveFile)
	r.Handle("GET /app", "", file)
	r.Handle("GET /app/kinodom.apk", "", file)
	r.Handle("GET /api/v1/app", "", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		in, ok := d.Info()
		if !ok {
			httpx.WriteError(w, http.StatusNotFound, "приложения для Android на сервере нет")
			return
		}
		httpx.WriteJSON(w, http.StatusOK, struct {
			Info
			URL string `json:"url"`
		}{in, "/app/" + apkName})
	}))
}

// fileName — имя файла для браузера ТВ: версия только из букв, цифр, точек и дефисов, иначе без неё.
func fileName(version string) string {
	for _, r := range version {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == '.' || r == '-') {
			return "Kinodom.apk"
		}
	}
	return "Kinodom-" + version + ".apk"
}

func (d *Dist) serveFile(w http.ResponseWriter, r *http.Request) {
	in, ok := d.Info()
	if !ok {
		httpx.WriteError(w, http.StatusNotFound, "приложения для Android на сервере нет")
		return
	}
	f, err := os.Open(filepath.Join(d.dir, apkName))
	if err != nil {
		httpx.WriteError(w, http.StatusNotFound, "приложения для Android на сервере нет")
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", apkType)
	w.Header().Set("Content-Disposition", `attachment; filename="`+fileName(in.Version)+`"`)
	http.ServeContent(w, r, "", fi.ModTime(), f)
}
