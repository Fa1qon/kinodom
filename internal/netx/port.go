package netx

import (
	"errors"
	"fmt"
	"net"
)

// FreeTCPUDPPort находит порт, свободный на host и для TCP, и для UDP (торрент-движок слушает
// оба на одном номере). Начинаем с UDP: эфемерный UDP-порт Windows не выдаёт из диапазонов,
// которые она зарезервировала под Hyper-V и WSL, а вот случайный TCP-порт туда попадает —
// и тогда UDP на том же номере не открывается («access forbidden»).
func FreeTCPUDPPort(host string) (int, error) {
	for i := 0; i < 50; i++ {
		pc, err := net.ListenPacket("udp4", net.JoinHostPort(host, "0"))
		if err != nil {
			return 0, err
		}
		port := pc.LocalAddr().(*net.UDPAddr).Port
		ln, err := net.Listen("tcp4", net.JoinHostPort(host, fmt.Sprint(port)))
		pc.Close()
		if err != nil {
			continue // этот номер занят по TCP — берём следующий
		}
		ln.Close()
		return port, nil
	}
	return 0, errors.New("не нашлось порта, свободного и для TCP, и для UDP")
}
