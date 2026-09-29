package netx

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"syscall"
	"time"
)

// ErrPrivateAddress — адрес этого ПК или домашней сети там, где ждут публичный сайт.
var ErrPrivateAddress = errors.New("адрес домашней сети или этого компьютера")

// PrivateHost — хост — это ПК или домашняя сеть: «localhost», IP loopback, частных диапазонов,
// link-local или нулевой. Имя домена (кроме localhost) не проверяется: его проверит PublicOnly при
// соединении — без лишнего запроса к DNS мимо прокси.
func PrivateHost(host string) bool {
	host = strings.Trim(strings.ToLower(host), "[]")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && privateIP(ip)
}

func privateIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified()
}

// PublicOnly — транспорт соединяется только с публичными адресами, кроме самого прокси p (он бывает
// в домашней сети). Адреса картинок берутся из описаний раздач публичных трекеров: без этого сервер
// ходил бы по ссылке из описания в домашнюю сеть (ревью 5b, M10). Проверяется адрес, с которым
// соединяются на самом деле, — после DNS, поэтому имя, указывающее в домашнюю сеть, не пройдёт.
func PublicOnly(t *http.Transport, p *Proxy) {
	inner := t.DialContext
	d := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second,
		Control: func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if ip := net.ParseIP(host); err == nil && ip != nil && privateIP(ip) {
				return fmt.Errorf("%w (%s)", ErrPrivateAddress, host)
			}
			return nil
		}}
	t.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if u := p.URL(); u != nil && strings.EqualFold(addr, u.Host) {
			return inner(ctx, network, addr)
		}
		return d.DialContext(ctx, network, addr)
	}
}
