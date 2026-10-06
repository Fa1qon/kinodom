//go:build !windows

package library

import "io/fs"

// hiddenAttrs — признак «скрытый/системный» есть только у Windows: на Android не смотрим
// (портирование сервера, план 2026-10-06).
func hiddenAttrs(fs.FileInfo) bool { return false }
