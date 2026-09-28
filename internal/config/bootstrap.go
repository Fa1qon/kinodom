package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Bootstrap — настройки, нужные до открытия базы. Файл kinodom.json читают все
// пользователи ПК (например, команда open), поэтому секретов здесь не бывает.
type Bootstrap struct {
	APIPort     int `json:"apiPort"`
	TorrentPort int `json:"torrentPort"`
}

func DefaultBootstrap() Bootstrap {
	return Bootstrap{APIPort: 8090, TorrentPort: 42000}
}

// LoadBootstrap читает kinodom.json. Если файла нет — создаёт его со значениями
// по умолчанию; поля, которых нет в файле, тоже получают значения по умолчанию.
func LoadBootstrap(path string) (Bootstrap, error) {
	b := DefaultBootstrap()
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return b, SaveBootstrap(path, b)
	}
	if err != nil {
		return Bootstrap{}, err
	}
	// PowerShell 5.1 (Set-Content -Encoding UTF8) и старый Блокнот пишут UTF-8 с BOM.
	data = bytes.TrimPrefix(data, []byte("\xEF\xBB\xBF"))
	if err := json.Unmarshal(data, &b); err != nil {
		return Bootstrap{}, fmt.Errorf("%s: файл повреждён или это не JSON (%v)", path, err)
	}
	if !validPort(b.APIPort) || !validPort(b.TorrentPort) {
		return Bootstrap{}, fmt.Errorf("%s: порты должны быть от 1 до 65535", path)
	}
	return b, nil
}

// SaveBootstrap пишет файл через временный, чтобы при сбое не остался обрывок.
func SaveBootstrap(path string, b Bootstrap) error {
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func validPort(p int) bool { return p > 0 && p <= 65535 }
