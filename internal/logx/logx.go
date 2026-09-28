// Package logx настраивает журнал: файл с ротацией (10 файлов по 10 МБ, спека, раздел 16)
// и, при работе в консоли, копия на экран.
package logx

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"gopkg.in/natefinch/lumberjack.v2"
)

func New(dir string, console bool) (*slog.Logger, io.Closer, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, nil, err
	}
	lj := &lumberjack.Logger{
		Filename:   filepath.Join(dir, "kinodom.log"),
		MaxSize:    10, // МБ
		MaxBackups: 9,  // + текущий = 10 файлов
		LocalTime:  true,
	}
	var w io.Writer = lj
	if console {
		w = io.MultiWriter(lj, os.Stderr)
	}
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelInfo})), lj, nil
}
