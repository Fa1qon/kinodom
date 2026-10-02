//go:build windows

package library

import (
	"errors"
	"unsafe"

	"golang.org/x/sys/windows"
)

// busy — файл открыт другой программой (плеер, проводник с предпросмотром).
func busy(err error) bool {
	return errors.Is(err, windows.ERROR_SHARING_VIOLATION) || errors.Is(err, windows.ERROR_LOCK_VIOLATION)
}

// Права на папку, которые нужны службе: создать файл и папку. «Удалить вложенное» (FILE_DELETE_CHILD) не
// спрашивается: «Разрешить доступ» даёт «Изменение» — в нём этого бита нет, удаление идёт по унаследованному
// DELETE на самих файлах (ревью 15Б, Critical 1).
const (
	fileAddFile         = 0x0002
	fileAddSubdirectory = 0x0004
)

var procAccessCheck = windows.NewLazySystemDLL("advapi32.dll").NewProc("AccessCheck")

// genericMapping — GENERIC_MAPPING для файлов и папок.
type genericMapping struct{ read, write, execute, all uint32 }

var fileMapping = genericMapping{0x120089, 0x120116, 0x1200A0, 0x1F01FF}

// writable — служба может создать в папке dir файл и папку: права папки проверяются против маркера процесса
// (AccessCheck), ничего не создавая. Не открытие папки с FILE_FLAG_BACKUP_SEMANTICS: у процесса с правами
// архивации (администратор) оно даёт запись мимо прав папки — проверка стала бы ложной (ревью 15Б).
func writable(dir string) bool {
	sd, err := windows.GetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.GROUP_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return false
	}
	var proc windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_DUPLICATE|windows.TOKEN_QUERY, &proc); err != nil {
		return false
	}
	defer proc.Close()
	var tok windows.Token // AccessCheck берёт только маркер олицетворения
	if err := windows.DuplicateTokenEx(proc, windows.TOKEN_QUERY, nil, windows.SecurityImpersonation, windows.TokenImpersonation, &tok); err != nil {
		return false
	}
	defer tok.Close()
	m := fileMapping
	var privs [256]byte
	privLen := uint32(len(privs))
	var granted, ok uint32
	r, _, _ := procAccessCheck.Call(uintptr(unsafe.Pointer(sd)), uintptr(tok), fileAddFile|fileAddSubdirectory,
		uintptr(unsafe.Pointer(&m)), uintptr(unsafe.Pointer(&privs[0])), uintptr(unsafe.Pointer(&privLen)),
		uintptr(unsafe.Pointer(&granted)), uintptr(unsafe.Pointer(&ok)))
	return r != 0 && ok != 0
}
