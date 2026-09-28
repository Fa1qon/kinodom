// Package web — файлы веб-пульта; встраиваются в kinodom.exe.
package web

import (
	"embed"
	"io/fs"
)

//go:embed static
var files embed.FS

// Static — корень пульта (содержимое папки static).
var Static = mustSub(files, "static")

func mustSub(f fs.FS, dir string) fs.FS {
	s, err := fs.Sub(f, dir)
	if err != nil {
		panic(err)
	}
	return s
}
