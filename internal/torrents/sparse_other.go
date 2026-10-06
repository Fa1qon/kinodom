//go:build !windows

package torrents

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// diskFree — свободное место на диске папки (портирование сервера, план 2026-10-06).
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
	var st unix.Statfs_t
	if err = unix.Statfs(dir, &st); err != nil {
		return 0, 0, err
	}
	// Bavail/Bsize — свободно с учётом прав; Bsize бывает отрицательным вSIGN — берём Frsize.
	bsize := int64(st.Bsize)
	if bsize <= 0 {
		bsize = int64(st.Frsize)
	}
	return int64(st.Bavail) * bsize, int64(st.Blocks) * bsize, nil
}

// createSparse — файл с заранее заданным размером: unix-версия создаёт обычный файл нужной длины
// (разреженность не обязательна: файловые системы Android и так откладывают блоки).
func createSparse(path string, size int64) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Truncate(size)
}

// zeroRange — освободить место куска: FALLOC_FL_PUNCH_HOLE | KEEP_SIZE (ext4/f2fs умеют; не умеет —
// обычная запись нулей).
func zeroRange(path string, size, from, to int64) error {
	if err := createSparse(path, size); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	if err = unix.Fallocate(int(f.Fd()), unix.FALLOC_FL_PUNCH_HOLE|unix.FALLOC_FL_KEEP_SIZE, from, to-from); err == nil {
		return nil
	}
	if _, err = f.WriteAt(make([]byte, to-from), from); err != nil && !errors.Is(err, os.ErrClosed) {
		return err
	}
	return nil
}

// IsNetworkPath — сетевой путь; на Android всё хранилище локальное.
func IsNetworkPath(string) bool { return false }
