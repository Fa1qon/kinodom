package app

import (
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"

	"kinodom/internal/httpx"
	"kinodom/internal/torrents"
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

// readDir — чтение папки от имени службы; usersRoot — папка профилей (тесты подменяют).
var (
	readDir   = os.ReadDir
	statDir   = os.Stat
	usersRoot = func() string {
		d := os.Getenv("SystemDrive")
		if d == "" {
			d = "C:"
		}
		return d + `\Users`
	}
)

// profileFolders — обычные папки пользователя: профиль службе не виден, а фильмы у людей — там.
var profileFolders = []string{"Desktop", "Downloads", "Videos"}

// handleDirs — обзор папок (спека этапа 11a, раздел 7): без path — диски этого ПК; иначе — подпапки
// по имени, без скрытых и системных. Файлов и их содержимого нет; сетевых путей — тоже.
func handleDirs(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query().Get("path")
	if p == "" {
		httpx.WriteJSON(w, http.StatusOK, dirsView{Dirs: []string{}, Drives: localDrives()})
		return
	}
	if !isDrivePath(p) || torrents.IsNetworkPath(p) {
		httpx.WriteError(w, http.StatusBadRequest, "нужна папка на диске этого компьютера, например D:\\Фильмы")
		return
	}
	dir := filepath.Clean(p)
	out := dirsView{Path: dir, Dirs: []string{}}
	if parent := filepath.Dir(dir); parent != dir {
		out.Parent = parent
	}
	fi, err := statDir(dir)
	if err != nil && !errors.Is(err, fs.ErrPermission) || err == nil && !fi.IsDir() {
		httpx.WriteError(w, http.StatusBadRequest, "папки "+dir+" нет")
		return
	}
	var entries []os.DirEntry
	if err == nil { // внутри закрытой папки служба не видит даже саму папку — это тоже «нет доступа»
		entries, err = readDir(dir)
	}
	if errors.Is(err, fs.ErrPermission) {
		out.Denied = true
		if strings.EqualFold(filepath.Dir(dir), filepath.Clean(usersRoot())) {
			out.Dirs = slices.Clone(profileFolders)
		}
		httpx.WriteJSON(w, http.StatusOK, out)
		return
	}
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "папка "+dir+" не открывается")
		return
	}
	for _, e := range entries {
		if e.IsDir() && !hiddenEntry(e) {
			out.Dirs = append(out.Dirs, e.Name())
		}
	}
	slices.SortFunc(out.Dirs, func(a, b string) int { return strings.Compare(strings.ToLower(a), strings.ToLower(b)) })
	httpx.WriteJSON(w, http.StatusOK, out)
}

// isDrivePath — полный путь на букве диска: «D:\…» (не «\\?\…», не относительный).
func isDrivePath(p string) bool {
	if len(p) < 3 || p[1] != ':' || (p[2] != '\\' && p[2] != '/') {
		return false
	}
	c := p[0] | 0x20
	return c >= 'a' && c <= 'z'
}

// hiddenEntry — скрытая или системная папка ($RECYCLE.BIN, System Volume Information).
func hiddenEntry(e os.DirEntry) bool {
	fi, err := e.Info()
	if err != nil {
		return true
	}
	a, ok := fi.Sys().(*syscall.Win32FileAttributeData)
	return ok && a.FileAttributes&(syscall.FILE_ATTRIBUTE_HIDDEN|syscall.FILE_ATTRIBUTE_SYSTEM) != 0
}

// localDrives — несетевые диски ПК со свободным местом (пустой картридер пропускается).
func localDrives() []driveView {
	mask, err := windows.GetLogicalDrives()
	if err != nil {
		return nil
	}
	var out []driveView
	for i := range 26 {
		if mask&(1<<i) == 0 {
			continue
		}
		root := string(rune('A'+i)) + `:\`
		rp, _ := windows.UTF16PtrFromString(root)
		if t := windows.GetDriveType(rp); t != windows.DRIVE_FIXED && t != windows.DRIVE_REMOVABLE {
			continue
		}
		var free uint64
		if windows.GetDiskFreeSpaceEx(rp, &free, nil, nil) != nil {
			continue
		}
		out = append(out, driveView{Path: root, Free: int64(free)})
	}
	return out
}
