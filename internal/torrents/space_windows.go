//go:build windows

package torrents

import "golang.org/x/sys/windows"

// diskFree — сколько места свободно на диске папки для этой учётной записи (с учётом квот).
func diskFree(dir string) (int64, error) {
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return 0, err
	}
	var free, total, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(p, &free, &total, &totalFree); err != nil {
		return 0, err
	}
	return int64(free), nil
}
