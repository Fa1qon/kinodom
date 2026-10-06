//go:build !windows

package edge

// На Android нет Microsoft Edge: пропуск Cloudflare добыть нельзя, Rutracker работает по API
// (портирование сервера, план 2026-10-06). UserAgent остаётся пустым — модуль Edge не включается.

// ExecPath — путь к msedge.exe; на Android Edge нет.
func ExecPath() string { return "" }

// userAgentOf — недостижимо: UserAgent падает ErrNoEdge раньше.
func userAgentOf(string) (string, error) { return "", ErrNoEdge }
