package winsvc

import (
	"errors"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// classes — ссылки scheme:// в root\base (для всех пользователей — HKLM\Software\Classes).
type classes struct {
	root registry.Key
	base string
}

func (c classes) key(scheme string, sub ...string) string {
	return strings.Join(append([]string{c.base, scheme}, sub...), `\`)
}

func (c classes) SetProtocol(scheme, command string) error {
	k, _, err := registry.CreateKey(c.root, c.key(scheme), registry.SET_VALUE)
	if err != nil {
		return err
	}
	err = k.SetStringValue("", "URL:Kinodom")
	if err == nil {
		err = k.SetStringValue("URL Protocol", "")
	}
	k.Close()
	if err != nil {
		return err
	}
	k, _, err = registry.CreateKey(c.root, c.key(scheme, "shell", "open", "command"), registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	return k.SetStringValue("", command)
}

func (c classes) DeleteProtocol(scheme string) error {
	for _, p := range []string{c.key(scheme, "shell", "open", "command"), c.key(scheme, "shell", "open"), c.key(scheme, "shell"), c.key(scheme)} {
		if err := registry.DeleteKey(c.root, p); err != nil && !errors.Is(err, registry.ErrNotExist) {
			return err
		}
	}
	return nil
}

// autorunKey — автозапуск при входе любого пользователя.
const autorunKey = `Software\Microsoft\Windows\CurrentVersion\Run`

func (c classes) SetAutorun(name, command string) error {
	k, _, err := registry.CreateKey(c.root, autorunKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	return k.SetStringValue(name, command)
}

func (c classes) DeleteAutorun(name string) error {
	k, err := registry.OpenKey(c.root, autorunKey, registry.SET_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer k.Close()
	if err := k.DeleteValue(name); err != nil && !errors.Is(err, registry.ErrNotExist) {
		return err
	}
	return nil
}

// KinodomProtocol — обработчик kinodom:// для всех пользователей (HKLM\Software\Classes, его ставит
// установщик): служба видит только его — HKCU у неё свой (хвост Х33).
func KinodomProtocol() bool {
	cmd, err := classes{root: registry.LOCAL_MACHINE, base: `Software\Classes`}.Protocol("kinodom")
	return err == nil && cmd != ""
}

func (c classes) Protocol(scheme string) (string, error) {
	k, err := registry.OpenKey(c.root, c.key(scheme, "shell", "open", "command"), registry.QUERY_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	defer k.Close()
	s, _, err := k.GetStringValue("")
	if errors.Is(err, registry.ErrNotExist) {
		return "", nil
	}
	return s, err
}
