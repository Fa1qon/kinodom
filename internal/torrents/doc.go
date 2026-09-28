// Package torrents — модуль «torrents»: торрент-движок, открытие раздач, буферизация
// и поток с перемоткой (спека, раздел 9).
package torrents

// envfirst.local инициализируется раньше пакета storage движка и включает классический
// ввод-вывод (см. third_party/envfirst).
import _ "envfirst.local"
