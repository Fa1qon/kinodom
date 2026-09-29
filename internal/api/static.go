package api

import (
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// pultTypes — типы файлов пульта. На Windows пакет mime берёт типы из реестра, и .js там бывает
// text/plain: браузер тогда не загрузит ES-модули пульта (этап 7b).
var pultTypes = map[string]string{
	".html":  "text/html; charset=utf-8",
	".js":    "text/javascript; charset=utf-8",
	".css":   "text/css; charset=utf-8",
	".woff2": "font/woff2",
	".svg":   "image/svg+xml",
	".txt":   "text/plain; charset=utf-8",
}

// pultHandler — файлы пульта из fsys. Кэш — с перепроверкой: после обновления kinodom.exe пульт
// сразу новый.
func pultHandler(fsys fs.FS) http.Handler {
	files := http.FileServerFS(fsys)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ext := strings.ToLower(path.Ext(r.URL.Path))
		if strings.HasSuffix(r.URL.Path, "/") {
			ext = ".html" // index.html папки
		}
		if t, ok := pultTypes[ext]; ok {
			w.Header().Set("Content-Type", t)
		}
		w.Header().Set("Cache-Control", "no-cache")
		files.ServeHTTP(w, r)
	})
}
