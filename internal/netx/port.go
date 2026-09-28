package netx

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
)

// FreeTCPUDPPort находит порт, свободный на host и для TCP, и для UDP (торрент-движок слушает
// оба на одном номере). Номер берётся случайно из 20000–48999, ниже динамического диапазона
// Windows (49152–65535): эфемерные порты Windows выдаёт подряд, а Hyper-V и WSL резервируют
// в этом диапазоне целые блоки по сотне портов отдельно для TCP и для UDP
// (netsh int ipv4 show excludedportrange) — «следующий свободный» раз за разом попадал в блок.
func FreeTCPUDPPort(host string) (int, error) {
	for i := 0; i < 200; i++ {
		port := 20000 + rand.IntN(29000)
		addr := net.JoinHostPort(host, fmt.Sprint(port))
		pc, err := net.ListenPacket("udp4", addr)
		if err != nil {
			continue // занят или зарезервирован для UDP
		}
		ln, err := net.Listen("tcp4", addr)
		pc.Close()
		if err != nil {
			continue // занят или зарезервирован для TCP
		}
		ln.Close()
		return port, nil
	}
	return 0, errors.New("не нашлось порта, свободного и для TCP, и для UDP")
}
