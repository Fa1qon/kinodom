package api

import (
	"net"
	"net/http"
	"os"
	"strings"

	"kinodom/internal/httpx"
)

// hostList — какие имена в заголовке Host допустимы: localhost, имя ПК и адреса его интерфейсов.
// Чужое имя в Host — признак DNS rebinding (страница в браузере притворяется нашим сервером), такие
// запросы отклоняются. Адреса ПК — из httpx: там же их знает FromThisPC, и смена адреса видна обоим.
type hostList struct {
	names map[string]bool
}

func newHostList() *hostList {
	m := map[string]bool{"localhost": true, "127.0.0.1": true, "::1": true}
	if name, err := os.Hostname(); err == nil {
		m[strings.ToLower(name)] = true
	}
	return &hostList{names: m}
}

func (h *hostList) allows(hostport string) bool {
	host := normalizeHost(hostport)
	if h.names[host] {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || httpx.IsOwnIP(ip))
}

// normalizeHost: "[::1]:8090" → "::1", "LocalHost:8090" → "localhost".
func normalizeHost(hostport string) string {
	host := hostport
	if hh, _, err := net.SplitHostPort(hostport); err == nil {
		host = hh
	}
	return strings.ToLower(strings.Trim(host, "[]"))
}

func (h *hostList) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !h.allows(r.Host) {
			httpx.WriteError(w, http.StatusMisdirectedRequest, "неизвестный адрес сервера: "+r.Host)
			return
		}
		next.ServeHTTP(w, r)
	})
}
