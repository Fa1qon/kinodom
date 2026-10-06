package app

import (
	"net/http"

	"kinodom/internal/httpx"
)

// driveView — диск этого ПК в обзоре папок.
type driveView struct {
	Path string `json:"path"` // «D:\»
	Free int64  `json:"free"` // свободно, байт
}

// dirsView — папка в обзоре: полный путь, родитель ("" — у корня диска: выше — список дисков) и
// имена подпапок. Denied — служба папку не читает: её можно выбрать и «Разрешить доступ».
type dirsView struct {
	Path   string      `json:"path,omitempty"`
	Parent string      `json:"parent"`
	Dirs   []string    `json:"dirs"`
	Denied bool        `json:"denied,omitempty"`
	Drives []driveView `json:"drives,omitempty"`
}

// dirsHandler — обзор папок (спека этапа 11a, раздел 7). Windows подставляет диски и подпапки
// этого ПК (dirs_windows.go); на Android папки задаёт само приложение — обзор пуст
// (портирование сервера, план 2026-10-06).
var dirsHandler = func(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, dirsView{Dirs: []string{}})
}

func handleDirs(w http.ResponseWriter, r *http.Request) { dirsHandler(w, r) }
