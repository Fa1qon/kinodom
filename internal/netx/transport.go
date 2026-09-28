package netx

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

// ErrProxyDown — не удалось подключиться к самому прокси. Это отдельная проблема от
// «трекер недоступен» (спека, раздел 16): перебирать зеркала бесполезно, чинить надо прокси.
var ErrProxyDown = errors.New("прокси не отвечает")

// NewTransport — транспорт для внешних сайтов. proxy — строка из настроек
// (socks5://… или http://…); пусто — напрямую: трафик идёт как у системы, через правила
// VPN-клиента. Переменные окружения HTTP_PROXY и т. п. не читаются: у службы их нет,
// а в консоли они не должны менять поведение.
func NewTransport(proxy string) (*http.Transport, error) {
	u, err := ParseProxy(proxy)
	if err != nil {
		return nil, err
	}
	d := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	t := &http.Transport{
		DialContext:           d.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          20,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   30 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
	if u == nil {
		return t, nil
	}
	t.Proxy = http.ProxyURL(u)
	// К прокси (и HTTP, и SOCKS5) net/http подключается через этот же DialContext —
	// так «не отвечает прокси» отличается от «не отвечает сайт за прокси».
	proxyAddr := u.Host
	t.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		conn, err := d.DialContext(ctx, network, addr)
		if err != nil && strings.EqualFold(addr, proxyAddr) {
			return nil, fmt.Errorf("%w (%s): %w", ErrProxyDown, proxyAddr, err)
		}
		return conn, err
	}
	return t, nil
}
