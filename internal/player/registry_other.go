//go:build !windows

package player

// На Android нет реестра Windows и внешних плееров с ним: поиск по PATH остаётся (find.go),
// установка ссылок kinodom:// не имеет смысла. Портирование сервера, план 2026-10-06.

// regString — строковое значение реестра по «HKLM\…» или «HKCU\…»; на Android реестра нет.
func regString(key, value string) string { return "" }

// ProtocolEntries — записи ссылки kinodom://; на Android их нет.
func ProtocolEntries(string) []ProtocolEntry { return nil }

// InstallProtocol — установить ссылку kinodom://; на Android нечего устанавливать.
func InstallProtocol(string) error { return nil }

// UninstallProtocol — убрать ссылку kinodom://; на Android нечего убирать.
func UninstallProtocol() error { return nil }
