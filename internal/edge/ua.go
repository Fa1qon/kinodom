// Package edge — скрытый Microsoft Edge, который добывает пропуск Cloudflare (cookie
// cf_clearance) для Rutracker. Браузер нужен только для этого: рабочие запросы идут обычным
// HTTP-клиентом с тем же User-Agent и полученными cookie (спека, раздел 6).
package edge

import (
	"errors"
)

// ErrNoEdge — Edge не установлен: пропуск Cloudflare добыть нельзя, Rutracker работает по API.
var ErrNoEdge = errors.New("Microsoft Edge не установлен — пропуск Cloudflare добыть нельзя")

// UserAgent — User-Agent установленного Edge. Пропуск Cloudflare привязан к UA (с другим —
// снова проверка), поэтому HTTP-клиент Rutracker шлёт ровно его, и Edge запускается с ним же.
func UserAgent() (string, error) {
	p := ExecPath()
	if p == "" {
		return "", ErrNoEdge
	}
	return userAgentOf(p)
}
