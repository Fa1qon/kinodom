//go:build windows

package library

import (
	"io/fs"
	"syscall"
)

// hiddenAttrs — файл скрытый или системный (Windows): медиатека такие не смотрит.
func hiddenAttrs(info fs.FileInfo) bool {
	if a, ok := info.Sys().(*syscall.Win32FileAttributeData); ok {
		return a.FileAttributes&(syscall.FILE_ATTRIBUTE_HIDDEN|syscall.FILE_ATTRIBUTE_SYSTEM) != 0
	}
	return false
}
