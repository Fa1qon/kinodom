//go:build windows

package library

import (
	"errors"

	"golang.org/x/sys/windows"
)

// busy — файл открыт другой программой (плеер, проводник с предпросмотром).
func busy(err error) bool {
	return errors.Is(err, windows.ERROR_SHARING_VIOLATION) || errors.Is(err, windows.ERROR_LOCK_VIOLATION)
}

// Права на папку, которые нужны службе: создать файл и папку, удалить вложенное.
const (
	fileAddFile         = 0x0002
	fileAddSubdirectory = 0x0004
	fileDeleteChild     = 0x0040
)

// writable — служба может писать в папку dir: папка открывается с правами записи, ничего не создавая.
func writable(dir string) bool {
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return false
	}
	h, err := windows.CreateFile(p, fileAddFile|fileAddSubdirectory|fileDeleteChild,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return false
	}
	windows.CloseHandle(h)
	return true
}
