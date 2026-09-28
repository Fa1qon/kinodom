// Package edge — скрытый Microsoft Edge, который добывает пропуск Cloudflare (cookie
// cf_clearance) для Rutracker. Браузер нужен только для этого: рабочие запросы идут обычным
// HTTP-клиентом с тем же User-Agent и полученными cookie (спека, раздел 6).
package edge

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
)

// ErrNoEdge — Edge не установлен: пропуск Cloudflare не добыть, Rutracker работает по API.
var ErrNoEdge = errors.New("Microsoft Edge не установлен — пропуск Cloudflare добыть нельзя")

var reVersionDir = regexp.MustCompile(`^(\d+)\.\d+\.\d+\.\d+$`)

// ExecPath — путь к msedge.exe; "" — Edge не установлен.
func ExecPath() string {
	for _, p := range []string{
		`C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`,
		`C:\Program Files\Microsoft\Edge\Application\msedge.exe`,
	} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// UserAgent — User-Agent установленного Edge. Пропуск Cloudflare привязан к UA (с другим —
// снова проверка), поэтому HTTP-клиент Rutracker шлёт ровно его, и Edge запускается с ним же.
// После обновления Edge — одна новая добыча пропуска.
func UserAgent() (string, error) {
	p := ExecPath()
	if p == "" {
		return "", ErrNoEdge
	}
	return userAgentFrom(filepath.Dir(p))
}

// userAgentFrom — UA по старшей версии из имён папок версий рядом с msedge.exe
// («154.0.4258.37» → 154), в том же сокращённом виде, в каком его шлёт браузер с окном.
func userAgentFrom(appDir string) (string, error) {
	entries, err := os.ReadDir(appDir)
	if err != nil {
		return "", err
	}
	best := 0
	for _, e := range entries {
		if m := reVersionDir.FindStringSubmatch(e.Name()); m != nil && e.IsDir() {
			if n, _ := strconv.Atoi(m[1]); n > best {
				best = n
			}
		}
	}
	if best == 0 {
		return "", fmt.Errorf("в %s нет папки с версией Edge", appDir)
	}
	return fmt.Sprintf("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/%d.0.0.0 Safari/537.36 Edg/%d.0.0.0", best, best), nil
}
