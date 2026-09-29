//go:build windows

package torrents

import (
	"errors"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modkernel32                = windows.NewLazySystemDLL("kernel32.dll")
	procGetCompressedFileSizeW = modkernel32.NewProc("GetCompressedFileSizeW")
)

// createSparse создаёт файл (или открывает существующий), ставит флаг «разрежённый»
// (FSCTL_SET_SPARSE) и задаёт размер без выделения места на диске. Без этого запись
// в конец большого файла заставляет NTFS заливать нулями всё перед ним: 15 с на файл 8 ГиБ.
func createSparse(path string, size int64) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	var ret uint32
	if err := windows.DeviceIoControl(windows.Handle(f.Fd()), windows.FSCTL_SET_SPARSE, nil, 0, nil, 0, &ret, nil); err != nil {
		return err
	}
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	if fi.Size() < size {
		return f.Truncate(size) // у разрежённого файла кластеры не выделяются
	}
	return nil
}

// isSparse — у файла стоит атрибут FILE_ATTRIBUTE_SPARSE_FILE.
func isSparse(path string) (bool, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return false, err
	}
	a, err := windows.GetFileAttributes(p)
	if err != nil {
		return false, err
	}
	return a&windows.FILE_ATTRIBUTE_SPARSE_FILE != 0, nil
}

// allocatedSize — сколько файл реально занимает на диске.
func allocatedSize(path string) (int64, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	var high uint32
	low, _, e := procGetCompressedFileSizeW.Call(uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(&high)))
	if uint32(low) == 0xFFFFFFFF {
		var errno windows.Errno
		if errors.As(e, &errno) && errno != 0 {
			return 0, e
		}
	}
	return int64(high)<<32 | int64(uint32(low)), nil
}

// zeroRange освобождает место под байтами [from, to) файла (FSCTL_SET_ZERO_DATA): читаются нули,
// кластеры возвращаются диску. Файл сначала делается разрежённым (createSparse), а если его нет
// (удалили руками) — создаётся пустой разрежённый размера size.
func zeroRange(path string, size, from, to int64) error {
	if err := createSparse(path, size); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	in := struct{ fileOffset, beyondFinalZero int64 }{from, to}
	var ret uint32
	return windows.DeviceIoControl(windows.Handle(f.Fd()), windows.FSCTL_SET_ZERO_DATA,
		(*byte)(unsafe.Pointer(&in)), uint32(unsafe.Sizeof(in)), nil, 0, &ret, nil)
}
