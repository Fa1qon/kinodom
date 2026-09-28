package netx

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ErrProxyDown — проблема самого прокси: к нему не подключиться или он отказал во входе.
// Это отдельная проблема от «трекер недоступен» (спека, раздел 16): перебирать зеркала
// бесполезно, чинить надо прокси.
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
	// HTTPS идёт через прокси командой CONNECT; 407 на неё — прокси отклонил логин или пароль.
	t.OnProxyConnectResponse = func(_ context.Context, _ *url.URL, _ *http.Request, res *http.Response) error {
		if res.StatusCode == http.StatusProxyAuthRequired {
			return errProxyAuth
		}
		return nil
	}
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

// proxyError — прокси ответил, но отказал. Для errors.Is это тоже ErrProxyDown: чинить надо
// настройки прокси, а не ждать трекер, и перебирать зеркала бесполезно.
type proxyError struct{ text string }

func (e *proxyError) Error() string        { return e.text }
func (e *proxyError) Is(target error) bool { return target == ErrProxyDown }

var errProxyAuth = &proxyError{"прокси отклонил логин или пароль — проверьте адрес прокси в настройках"}

// proxyRefusal — отказ прокси при входе, если err — он; иначе nil. HTTP-прокси отказывает
// ответом 407 (см. OnProxyConnectResponse и once), SOCKS5 — ошибкой рукопожатия.
func proxyRefusal(err error) error {
	var op *net.OpError
	if errors.As(err, &op) && op.Op == "socks connect" && op.Err != nil {
		if msg := op.Err.Error(); strings.Contains(msg, "authentication") || strings.Contains(msg, "username/password") {
			return errProxyAuth
		}
	}
	return nil
}
