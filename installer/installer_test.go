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
		// Приложение для Android рядом с программой — сервер раздаёт его (спека этапа 13, 5.3); сборка без
		// Android SDK — без него.
		`Source: "..\bin\kinodom.apk"; DestDir: "{app}"; Flags: ignoreversion skipifsourcedoesntexist`,
		`Source: "..\bin\kinodom.apk.json"; DestDir: "{app}"; Flags: ignoreversion skipifsourcedoesntexist`,
		// Свой плеер — урезанный ffmpeg рядом с программой (план 18А).
		`Source: "..\bin\ffmpeg.exe"; DestDir: "{app}"; Flags: ignoreversion`,
		`Source: "..\bin\ffprobe.exe"; DestDir: "{app}"; Flags: ignoreversion`,
		// LGPL: текст лицензии и откуда исходники — рядом с ffmpeg (ревью 18А).
		`Source: "..\third_party\ffmpeg\LICENSE.LGPLv2.1.txt"; DestDir: "{app}"; DestName: "ffmpeg-LICENSE.LGPLv2.1.txt"; Flags: ignoreversion`,
		`Source: "..\third_party\ffmpeg\README.md"; DestDir: "{app}"; DestName: "ffmpeg-README.md"; Flags: ignoreversion`,
		"function PrepareToInstall",
		"Exec(Exe, 'stop'",
		"'install ' + InstallParams(",
		"' --downloads-default '",               // сохранённую папку загрузок не перезаписывает (ревью C1)
		`Uninstall\{' + '{#AppGuid}' + '}_is1'`, // ключ удаления — со скобками, как AppId (ревью C1)
		`{commonappdata}\Kinodom\data\kinodom.db`,
		"StoppedByUs := True", // остановил службу — вернёт её, если установка не дошла до конца (I3)
		"Exec(ExpandConstant('{app}\\kinodom.exe'), 'install', ",
		// Неудача после копирования без прежней программы (а база от «удаления без данных» может
		// быть) — удаление себя, иначе полуустановка (второе ревью, I-1).
		`else if CopyStarted and FileExists(ExpandConstant('{app}\unins000.exe')) then`,
		// kinodom uninstall — после стандартного «Вы действительно хотите удалить?»; отказ — Abort
		// до удаления файлов (второе ревью, I-2).
		"procedure CurUninstallStepChanged", "CurUninstallStep <> usUninstall",
		"Abort;",
		"'uninstall --purge'",
		"Удалить также всё скачанное Kinodom (и в папках медиатеки) и настройки?", // ревью 14В
		"MB_DEFBUTTON2",
		"Kinodom.url",
		"Открыть Kinodom",
		// Отказ до копирования и без полуустановки (спека этапа 11a, раздел 5.1): исключение в
		// AfterInstall Inno не откатывает — проверено первой живой установкой.
		"ExtractTemporaryFile('kinodom.exe')",
		"'install --check ' + InstallParams(",
		"CurStep = ssPostInstall",
		"'/VERYSILENT /SUPPRESSMSGBOXES /NORESTART'",
		"Check: InstallSucceeded",
		// Значок в трее и логотип (спека этапа 11a, раздел 5.2).
		"SetupIconFile=kinodom.ico",
		`Source: "kinodom.ico"`,
		`IconFilename: "{app}\kinodom.ico"`,
		`Parameters: "tray --open"`,
		// Значок — сразу после удачного kinodom install, и в тихом обновлении: строка [Run] без
		// postinstall выполняется до ssPostInstall и проверку «установка прошла» не проходит
		// (проверено тихим обновлением на этом ПК).
		`ExecAsOriginalUser(ExpandConstant('{app}\kinodomw.exe'), 'tray'`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("в kinodom.iss нет %q", want)
		}
	}
	for _, banned := range []string{"AfterInstall:", "RaiseException(", `Parameters: "tray"; Flags: runasoriginaluser`,
		"function InitializeUninstall", "CopyStarted and not WasInstalled"} {
		if strings.Contains(s, banned) {
			t.Errorf("в kinodom.iss есть %q — см. комментарии к списку обязательного", banned)
		}
	}
}
