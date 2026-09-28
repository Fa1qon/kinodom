//go:build windows

package api

import (
	"context"
	"errors"
	"fmt"
	"net"
	"syscall"
)

// ErrPortBusy — порт API уже занят (работающей службой Kinodom или другой программой).
var ErrPortBusy = errors.New("порт занят")

// soExclusiveAddrUse — SO_EXCLUSIVEADDRUSE, в Windows это ~SO_REUSEADDR = -5.
// Без него Windows разрешает занять порт двухстековым сокетом, даже если другая программа
// уже слушает его по IPv4 или на одном адресе: сервер писал бы «работает», а телевизоры
// попадали бы в чужую программу.
const soExclusiveAddrUse = -5

// Коды ошибок Winsock.
const (
	wsaEADDRINUSE = syscall.Errno(10048) // адрес занят
	wsaEACCES     = syscall.Errno(10013) // доступ запрещён: порт зарезервирован Windows или защитой
)

func listenExclusive(addr string) (net.Listener, error) {
	lc := net.ListenConfig{Control: func(_, _ string, c syscall.RawConn) error {
		var serr error
		if err := c.Control(func(fd uintptr) {
			serr = syscall.SetsockoptInt(syscall.Handle(fd), syscall.SOL_SOCKET, soExclusiveAddrUse, 1)
		}); err != nil {
			return err
		}
		return serr
	}}
	ln, err := lc.Listen(context.Background(), "tcp", addr)
	if err != nil {
		return nil, listenError(addr, err)
	}
	return ln, nil
}

// listenError объясняет по-русски, почему порт не открылся; текст ошибки ОС сохраняется.
func listenError(addr string, err error) error {
	port := addr
	if _, p, perr := net.SplitHostPort(addr); perr == nil {
		port = p
	}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		switch errno {
		case wsaEADDRINUSE:
			return fmt.Errorf("%w: порт %s занят — Kinodom уже запущен (служба или другая копия) либо порт занят другой программой (%v)", ErrPortBusy, port, err)
		case wsaEACCES:
			return fmt.Errorf("порт %s недоступен: его зарезервировала Windows (Hyper-V, WSL) или запрещает защита; список занятых диапазонов — netsh int ipv4 show excludedportrange protocol=tcp (%v)", port, err)
		}
	}
	return fmt.Errorf("не удалось открыть порт %s: %w", port, err)
}
