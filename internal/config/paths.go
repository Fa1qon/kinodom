// Package config знает, где лежат файлы Kinodom, и читает начальную конфигурацию
// (то, что нужно до открытия базы). Остальные настройки хранятся в базе.
package config

import (
	"os"
	"path/filepath"
)

// EnvHome переопределяет корневую папку — для разработки и тестов.
const EnvHome = "KINODOM_HOME"

// Paths — все места на диске, которые использует сервер.
type Paths struct {
	Home        string // %ProgramData%\Kinodom
	Bootstrap   string // Home\kinodom.json — читается всеми, без секретов
	Data        string // Home\data — только SYSTEM и Administrators (права ставит инсталлятор)
	DB          string // Data\kinodom.db
	Logs        string // Data\logs
	EdgeProfile string // Data\edge-profile — профиль Edge для пропуска Cloudflare
	Images      string // Data\images — постеры и логотипы
	Torrent     string // Data\torrent — отметки кусков и узлы DHT
	IPTV        string // Data\iptv — телепрограмма и база iptv-org (этап 8)
	Logos       string // Data\logos — логотипы каналов: отдельно от постеров, их чистит каталог
}

// DefaultHome — KINODOM_HOME, иначе %ProgramData%\Kinodom.
func DefaultHome() string {
	if h := os.Getenv(EnvHome); h != "" {
		return h
	}
	pd := os.Getenv("ProgramData")
	if pd == "" {
		pd = `C:\ProgramData`
	}
	return filepath.Join(pd, "Kinodom")
}

func NewPaths(home string) Paths {
	data := filepath.Join(home, "data")
	return Paths{
		Home:        home,
		Bootstrap:   filepath.Join(home, "kinodom.json"),
		Data:        data,
		DB:          filepath.Join(data, "kinodom.db"),
		Logs:        filepath.Join(data, "logs"),
		EdgeProfile: filepath.Join(data, "edge-profile"),
		Images:      filepath.Join(data, "images"),
		Torrent:     filepath.Join(data, "torrent"),
		IPTV:        filepath.Join(data, "iptv"),
		Logos:       filepath.Join(data, "logos"),
	}
}

// Ensure создаёт папки, которых ещё нет.
func (p Paths) Ensure() error {
	for _, d := range []string{p.Home, p.Data, p.Logs, p.Images, p.Torrent, p.IPTV, p.Logos} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	return nil
}
