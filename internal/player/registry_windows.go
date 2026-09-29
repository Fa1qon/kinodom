package player

import (
	"errors"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// regString — строковое значение реестра по «HKLM\…» или «HKCU\…»; "" — нет ключа или значения.
func regString(key, value string) string {
	root, path, ok := strings.Cut(key, `\`)
	if !ok {
		return ""
	}
	base := registry.LOCAL_MACHINE
	if root == "HKCU" {
		base = registry.CURRENT_USER
	}
	k, err := registry.OpenKey(base, path, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()
	s, _, err := k.GetStringValue(value)
	if err != nil {
		return ""
	}
	return s
}

// protocolKey — ключ ссылки kinodom:// у текущего пользователя: права администратора не нужны.
const protocolKey = `Software\Classes\kinodom`

// ProtocolEntries — что записывается в реестр для ссылки kinodom://: ключ (от protocolKey) →
// значение по умолчанию и дополнительные значения.
func ProtocolEntries(exe string) []ProtocolEntry {
	return []ProtocolEntry{
		{Key: "", Default: "URL:Kinodom", Values: map[string]string{"URL Protocol": ""}},
		{Key: `shell\open\command`, Default: `"` + exe + `" open "%1"`},
	}
}

// ProtocolEntry — ключ реестра ссылки kinodom://.
type ProtocolEntry struct {
	Key     string
	Default string
	Values  map[string]string
}

// InstallProtocol регистрирует kinodom:// у текущего пользователя: браузер на этом ПК открывает такие
// ссылки командой «kinodom open». Инсталлятор (этап 11) делает то же для всех.
func InstallProtocol(exe string) error {
	for _, e := range ProtocolEntries(exe) {
		p := protocolKey
		if e.Key != "" {
			p += `\` + e.Key
		}
		k, _, err := registry.CreateKey(registry.CURRENT_USER, p, registry.SET_VALUE)
		if err != nil {
			return err
		}
		err = k.SetStringValue("", e.Default)
		for name, v := range e.Values {
			if err == nil {
				err = k.SetStringValue(name, v)
			}
		}
		k.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// UninstallProtocol убирает kinodom:// у текущего пользователя.
func UninstallProtocol() error {
	for _, p := range []string{protocolKey + `\shell\open\command`, protocolKey + `\shell\open`, protocolKey + `\shell`, protocolKey} {
		if err := registry.DeleteKey(registry.CURRENT_USER, p); err != nil && !errors.Is(err, registry.ErrNotExist) {
			return err
		}
	}
	return nil
}
