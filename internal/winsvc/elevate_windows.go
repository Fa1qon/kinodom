package winsvc

import (
	"errors"
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var procShellExecuteExW = windows.NewLazySystemDLL("shell32.dll").NewProc("ShellExecuteExW")

// shellExecuteInfo — SHELLEXECUTEINFOW.
type shellExecuteInfo struct {
	cbSize         uint32
	fMask          uint32
	hwnd           uintptr
	lpVerb         *uint16
	lpFile         *uint16
	lpParameters   *uint16
	lpDirectory    *uint16
	nShow          int32
	hInstApp       uintptr
	lpIDList       uintptr
	lpClass        *uint16
	hkeyClass      uintptr
	dwHotKey       uint32
	hIconOrMonitor uintptr
	hProcess       windows.Handle
}

const (
	seeMaskNoCloseProcess = 0x00000040
	seeMaskNoAsync        = 0x00000100
	swHide                = 0
)

// Elevate запускает exe с аргументами от администратора (окно Windows «Да/Нет») и ждёт его
// завершения. Нажали «Нет» — ErrCancelled; код выхода не 0 — ошибка.
func Elevate(exe string, args []string) error {
	params := ""
	for i, a := range args {
		if i > 0 {
			params += " "
		}
		params += syscall.EscapeArg(a)
	}
	verb, _ := windows.UTF16PtrFromString("runas")
	file, err := windows.UTF16PtrFromString(exe)
	if err != nil {
		return err
	}
	par, err := windows.UTF16PtrFromString(params)
	if err != nil {
		return err
	}
	info := shellExecuteInfo{fMask: seeMaskNoCloseProcess | seeMaskNoAsync, lpVerb: verb, lpFile: file, lpParameters: par, nShow: swHide}
	info.cbSize = uint32(unsafe.Sizeof(info))
	if r, _, e := procShellExecuteExW.Call(uintptr(unsafe.Pointer(&info))); r == 0 {
		if errors.Is(e, windows.ERROR_CANCELLED) {
			return ErrCancelled
		}
		return fmt.Errorf("запуск от администратора: %w", e)
	}
	if info.hProcess == 0 {
		return errors.New("запуск от администратора: нет процесса")
	}
	defer windows.CloseHandle(info.hProcess)
	if _, err := windows.WaitForSingleObject(info.hProcess, windows.INFINITE); err != nil {
		return err
	}
	var code uint32
	if err := windows.GetExitCodeProcess(info.hProcess, &code); err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("команда от администратора завершилась с кодом %d", code)
	}
	return nil
}
