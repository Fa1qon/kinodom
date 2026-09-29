//go:build windows

package torrents

import (
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// isNetworkPath — путь на сетевом диске: \сервер\папка или подключённый сетевой диск (Z:).
func isNetworkPath(dir string) bool {
	switch {
	case strings.HasPrefix(dir, `\?\UNC\`):
		return true
	case strings.HasPrefix(dir, `\?\`), strings.HasPrefix(dir, `\.\`):
		dir = dir[4:]
	case strings.HasPrefix(dir, `\`):
		return true
	}
	vol := filepath.VolumeName(dir)
	if vol == "" {
		return false
	}
	p, err := windows.UTF16PtrFromString(vol + `\`)
	if err != nil {
		return false
	}
	return windows.GetDriveType(p) == windows.DRIVE_REMOTE
}
