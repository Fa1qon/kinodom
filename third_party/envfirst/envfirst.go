// Package envfirst задаёт переменные окружения, которые библиотеки читают в своих init().
//
// Почему это работает: Go инициализирует пакеты по порядку их путей импорта, как только
// готовы их зависимости (правило действует с Go 1.21). Путь "envfirst.local" при сортировке
// идёт раньше "github.com/…", а зависит этот пакет только от "os" — поэтому его init()
// выполняется раньше init() пакета github.com/anacrolix/torrent/storage.
package envfirst

import "os"

func init() {
	// Классический ввод-вывод вместо mmap: с mmap движок держит файлы открытыми до выхода
	// процесса, и их нельзя удалить (проверено в spikes/torrent-stream). Классический
	// к тому же пишет на Windows в разы быстрее.
	if os.Getenv("TORRENT_STORAGE_DEFAULT_FILE_IO") == "" {
		os.Setenv("TORRENT_STORAGE_DEFAULT_FILE_IO", "classic")
	}
}
