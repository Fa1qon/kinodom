//go:build !windows

package library

import "os"

func busy(error) bool { return false }

// writable — служба может писать в папку dir: проба файлом (без Windows прав без записи не узнать).
func writable(dir string) bool {
	f, err := os.CreateTemp(dir, ".kinodom-*.tmp")
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return true
}
