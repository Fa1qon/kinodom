package app

import (
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"kinodom/internal/httpx"
	"kinodom/internal/torrents"
)

// accessView — ответ «чья это папка» для «Разрешить доступ» (спека этапа 11a, раздел 4.7): kind —
// library (папка медиатеки), downloads (папка загрузок) или "" (не из настроек: права не выдаются);
// readable — служба её читает.
type accessView struct {
	Kind     string `json:"kind"`
	Readable bool   `json:"readable"`
}

// handleAccess — GET /api/v1/library/access?path=… только с этого ПК: его спрашивает kinodomw.exe
// перед окном Windows «Да/Нет». Совпадение — точное (без учёта регистра): вложенная или соседняя
// папка — чужая.
func (a *App) handleAccess(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query().Get("path")
	var out accessView
	if p == "" || !filepath.IsAbs(p) || torrents.IsNetworkPath(p) {
		httpx.WriteJSON(w, http.StatusOK, out)
		return
	}
	key := folderKey(p)
	if key == folderKey(a.Settings.Current().DownloadsDir) {
		out.Kind = "downloads"
	} else {
		cs, err := a.Library.Categories(r.Context(), "")
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, err.Error())
			return
		}
		for _, c := range cs {
			for _, f := range c.Folders {
				if folderKey(f.Path) == key {
					out.Kind = "library"
				}
			}
		}
	}
	if out.Kind != "" {
		out.Readable = readable(p)
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// folderKey — путь для сравнения: полный, без «..» и «\» на конце, без учёта регистра.
func folderKey(p string) string {
	return strings.ToLower(strings.TrimRight(filepath.Clean(p), `\/`))
}

// readable — служба открывает папку и читает её список.
func readable(dir string) bool {
	f, err := os.Open(dir)
	if err != nil {
		return false
	}
	defer f.Close()
	_, err = f.Readdirnames(1)
	return err == nil || errors.Is(err, io.EOF)
}
