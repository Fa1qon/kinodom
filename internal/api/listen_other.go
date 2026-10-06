//go:build !windows

package api

import "net"

// listenExclusive — Windows слушает порт с SO_EXCLUSIVEADDRUSE, чтобы второй экземпляр не встал
// рядом; на Android достаточно обычного Listen (портирование сервера, план 2026-10-06).
func listenExclusive(addr string) (net.Listener, error) {
	return net.Listen("tcp", addr)
}
