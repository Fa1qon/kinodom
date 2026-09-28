package api

import (
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"kinodom/internal/httpx"
)

// hostList — какие имена в заголовке Host допустимы: localhost, адреса интерфейсов ПК
// и его имя. Чужое имя в Host — признак DNS rebinding (страница в браузере
// притворяется нашим сервером), такие запросы отклоняются.
type hostList struct {
	mu          sync.Mutex
	allowed     map[string]bool
	refreshedAt time.Time
}

func newHostList() *hostList {
	h := &hostList{}
	h.refreshLocked()
	return h
}

func (h *hostList) refreshLocked() {
	m := map[string]bool{"localhost": true, "127.0.0.1": true, "::1": true}
	if name, err := os.Hostname(); err == nil {
		m[strings.ToLower(name)] = true
	}
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok {
				m[ipn.IP.String()] = true
			}
		}
	}
	h.allowed = m
	h.refreshedAt = time.Now()
}

func (h *hostList) allows(hostport string) bool {
	host := normalizeHost(hostport)
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.allowed[host] {
		return true
	}
	// Адрес мог появиться недавно (сменился IP) — перечитаем, но не чаще раза в 10 с.
	if time.Since(h.refreshedAt) > 10*time.Second {
		h.refreshLocked()
		return h.allowed[host]
	}
	return false
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
