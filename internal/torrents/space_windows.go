//go:build windows

package torrents

import "golang.org/x/sys/windows"

// diskFree — сколько места свободно на диске папки для этой учётной записи (с учётом квот).
func diskFree(dir string) (int64, error) {
	free, _, err := diskSpace(dir)
	return free, err
}

// diskTotal — размер диска папки.
func diskTotal(dir string) (int64, error) {
	_, total, err := diskSpace(dir)
	return total, err
}

func diskSpace(dir string) (free, total int64, err error) {
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return 0, 0, err
	}
	var f, t, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(p, &f, &t, &totalFree); err != nil {
		return 0, 0, err
	}
	return int64(f), int64(t), nil
}
