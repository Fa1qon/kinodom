package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"kinodom/internal/winsvc"
)

// gui — сборка kinodomw.exe (-H windowsgui, -X main.gui=1): консоли нет, ошибки ссылок kinodom://
// показываются окном Windows, подробности — в журнал пользователя (спека этапа 11a, раздел 4.6).
var gui = ""

// Окно, повышение прав — через переменные: тесты подменяют их.
var (
	messageBox = showMessage
	elevate    = winsvc.Elevate
)

// maxOpenLog — журнал open.log не больше 1 МБ: больше — начинается заново.
const maxOpenLog = 1 << 20

// report — ошибка для человека: в консоль — полностью; в kinodomw.exe — ещё окно с коротким текстом
// без технических подробностей и строка в журнал.
func report(stderr io.Writer, text string, err error) int {
	fmt.Fprintln(stderr, "Ошибка:", err)
	if gui != "" {
		writeOpenLog(err)
		messageBox(text)
	}
	return 1
}

// writeOpenLog — строка в %LOCALAPPDATA%\Kinodom\open.log.
func writeOpenLog(err error) {
	dir := os.Getenv("LOCALAPPDATA")
	if dir == "" {
		return
	}
	dir = filepath.Join(dir, "Kinodom")
	if os.MkdirAll(dir, 0o755) != nil {
		return
	}
	path := filepath.Join(dir, "open.log")
	flag := os.O_CREATE | os.O_WRONLY | os.O_APPEND
	if fi, e := os.Stat(path); e == nil && fi.Size() > maxOpenLog {
		flag |= os.O_TRUNC
	}
	f, e := os.OpenFile(path, flag, 0o644)
	if e != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %v\n", time.Now().Format("2006-01-02 15:04:05"), err)
}
