// Package installer — только проверка скрипта установщика Inno Setup (kinodom.iss).
package installer

import (
	"os"
	"strings"
	"testing"
)

// Правка скрипта не должна потерять обязательное (спека этапа 11a, раздел 5): постоянный AppId
// (иначе обновление поверх станет второй установкой), Windows 10+ x64, права администратора,
// русский язык, остановку службы до замены файлов, вызовы команд kinodom.exe.
func TestInstallerScript(t *testing.T) {
	b, err := os.ReadFile("kinodom.iss")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{
		`#define AppGuid "8F6A5C2E-4B1D-4E7A-9C3F-2D5B6A7E8F90"`,
		"AppId={{{#AppGuid}}",
		"MinVersion=10.0",
		"ArchitecturesAllowed=x64compatible",
		"PrivilegesRequired=admin",
		`compiler:Languages\Russian.isl`,
		`Source: "..\bin\kinodom.exe"`,
		`Source: "..\bin\kinodomw.exe"`,
		"function PrepareToInstall",
		"Exec(Exe, 'stop'",
		"'install --result '",
		"' --downloads '",
		"'uninstall --purge'",
		"Удалить также скачанное и настройки?",
		"MB_DEFBUTTON2",
		"Kinodom.url",
		"Открыть Kinodom",
		"RaiseException(",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("в kinodom.iss нет %q", want)
		}
	}
}
