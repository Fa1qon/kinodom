package torrents

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/anacrolix/torrent/metainfo"
)

// torrentDirs — в какой папке загрузок лежит каждая раздача. Новые — в текущей (из настроек),
// старые — там, куда качались: после смены настройки их файлы доживают свой срок на старом месте
// (спека, раздел 9).
type torrentDirs struct {
	mu  sync.Mutex
	m   map[metainfo.Hash]string
	def string
}

func newTorrentDirs(def string) *torrentDirs {
	return &torrentDirs{m: map[metainfo.Hash]string{}, def: def}
}

func (d *torrentDirs) set(ih metainfo.Hash, dir string) {
	if dir == "" {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.m[ih] = dir
}

func (d *torrentDirs) get(ih metainfo.Hash) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if dir, ok := d.m[ih]; ok {
		return dir
	}
	return d.def
}

// setDefault — папка для новых раздач (смена в настройках, этап 7).
func (d *torrentDirs) setDefault(dir string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.def = dir
}

func (d *torrentDirs) defaultDir() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.def
}

// sameVolume — две папки на одном диске: место считается по диску.
func sameVolume(a, b string) bool {
	return strings.EqualFold(filepath.VolumeName(a), filepath.VolumeName(b))
}

// CheckDownloadsDir проверяет папку загрузок до запуска движка и при смене в настройках, объясняя
// отказ человеческим языком (хвост этапа 2): сетевой диск, нет папки, нет права записи, диск без
// разрежённых файлов.
func CheckDownloadsDir(dir string) error {
	return checkDownloadsDirWith(dir, createSparse)
}

func checkDownloadsDirWith(dir string, sparse func(path string, size int64) error) error {
	if IsNetworkPath(dir) {
		return fmt.Errorf("папка загрузок %s — на сетевом диске, а торренты качаются только на диски этого компьютера", dir)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return dirError(dir, err)
	}
	probe := filepath.Join(dir, ".kinodom-probe.tmp")
	if err := os.WriteFile(probe, []byte("kinodom"), 0o644); err != nil {
		return dirError(dir, err)
	}
	defer os.Remove(probe)
	if err := sparse(probe, 1<<20); err != nil {
		return fmt.Errorf("папка загрузок %s — на диске без разрежённых файлов (обычно exFAT или FAT32): нужен диск NTFS (%v)", dir, err)
	}
	return nil
}

func dirError(dir string, err error) error {
	if errors.Is(err, fs.ErrPermission) {
		return fmt.Errorf("нет права записи в папку загрузок %s", dir)
	}
	return fmt.Errorf("папка загрузок %s недоступна: %w", dir, err)
}
