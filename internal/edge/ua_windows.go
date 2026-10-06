//go:build windows

package edge

import (
	"errors"
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

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

// userAgentOf — UA по старшей части версии самого exe, в том же сокращённом виде, в каком его
// шлёт браузер с окном. Не по папкам версий: пока обновление ждёт перезапуска, рядом уже лежит
// папка новой версии, а запускается старый exe.
func userAgentOf(exe string) (string, error) {
	major, err := exeMajorVersion(exe)
	if err != nil {
		return "", fmt.Errorf("версия Edge (%s): %w", exe, err)
	}
	return fmt.Sprintf("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/%d.0.0.0 Safari/537.36 Edg/%d.0.0.0", major, major), nil
}

// exeMajorVersion — старшая часть версии файла из его ресурсов (VS_FIXEDFILEINFO): 154 у
// 154.0.4258.37.
func exeMajorVersion(path string) (int, error) {
	size, err := windows.GetFileVersionInfoSize(path, nil)
	if err != nil {
		return 0, err
	}
	buf := make([]byte, size)
	if err := windows.GetFileVersionInfo(path, 0, size, unsafe.Pointer(&buf[0])); err != nil {
		return 0, err
	}
	var fixed *windows.VS_FIXEDFILEINFO
	var n uint32
	if err := windows.VerQueryValue(unsafe.Pointer(&buf[0]), `\`, unsafe.Pointer(&fixed), &n); err != nil {
		return 0, err
	}
	major := int(fixed.FileVersionMS >> 16)
	if major == 0 {
		return 0, errors.New("в файле нет номера версии")
	}
	return major, nil
}
