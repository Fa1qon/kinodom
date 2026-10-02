package api

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"sync"
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
// сразу новый. ETag — хэш содержимого (план 16А): у встроенных файлов нет даты, и без ETag приложение
// при каждом запуске качало пульт целиком; с ним — 304.
func pultHandler(fsys fs.FS) http.Handler {
	files := http.FileServerFS(fsys)
	var tags sync.Map // имя файла → ETag (файлы встроены и не меняются, пока работает сервер)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ext := strings.ToLower(path.Ext(r.URL.Path))
		name := strings.TrimPrefix(r.URL.Path, "/")
		if strings.HasSuffix(r.URL.Path, "/") {
			ext = ".html" // index.html папки
			name += "index.html"
		}
		if t, ok := pultTypes[ext]; ok {
			w.Header().Set("Content-Type", t)
		}
		w.Header().Set("Cache-Control", "no-cache")
		if tag, ok := tags.Load(name); ok {
			w.Header().Set("ETag", tag.(string))
		} else if b, err := fs.ReadFile(fsys, name); err == nil {
			sum := sha256.Sum256(b)
			tag := `"` + hex.EncodeToString(sum[:8]) + `"`
			tags.Store(name, tag)
			w.Header().Set("ETag", tag)
		}
		files.ServeHTTP(w, r)
	})
}
