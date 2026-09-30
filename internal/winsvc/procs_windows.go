package winsvc

import (
	"errors"
	"os"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// procs — процессы программы: значки в трее всех вошедших пользователей держат kinodomw.exe, а
// установщик заменяет его, программа удаления — удаляет.
type procs struct{}

func (procs) Close(exe string) error {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(snap)
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	self := uint32(os.Getpid())
	var errs []error
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		if e.ProcessID == self || !strings.EqualFold(windows.UTF16ToString(e.ExeFile[:]), baseName(exe)) {
			continue
		}
		h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_TERMINATE|windows.SYNCHRONIZE, false, e.ProcessID)
		if err != nil {
			continue // чужой процесс с тем же именем, открыть нельзя — не наш
		}
		if strings.EqualFold(imagePath(h), exe) {
			if err := windows.TerminateProcess(h, 0); err != nil {
				errs = append(errs, err)
			} else {
				windows.WaitForSingleObject(h, 5000) // файл освобождается, когда процесс завершён
			}
		}
		windows.CloseHandle(h)
	}
	return errors.Join(errs...)
}

func baseName(p string) string {
	if i := strings.LastIndexAny(p, `\/`); i >= 0 {
		return p[i+1:]
	}
	return p
}

func imagePath(h windows.Handle) string {
	buf := make([]uint16, windows.MAX_LONG_PATH)
	size := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &size); err != nil {
		return ""
	}
	return windows.UTF16ToString(buf[:size])
}
