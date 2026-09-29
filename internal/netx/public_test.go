package netx

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestPrivateHost(t *testing.T) {
	for host, want := range map[string]bool{
		"localhost": true, "LOCALHOST": true, "printer.localhost": true, "127.0.0.1": true, "[::1]": true,
		"192.168.1.1": true, "10.0.0.5": true, "172.16.3.4": true, "169.254.1.1": true, "0.0.0.0": true,
		"i8.imageban.ru": false, "8.8.8.8": false, "fastpic.org": false, "172.32.0.1": false,
	} {
		if got := PrivateHost(host); got != want {
			t.Errorf("%s: %v", host, got)
		}
	}
}

// Транспорт «только публичные адреса» не соединяется с этим ПК и домашней сетью — проверяется адрес
// соединения, после DNS; сам прокси из домашней сети разрешён.
func TestPublicOnlyRefusesPrivateTargets(t *testing.T) {
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "сайт") }))
	defer site.Close()
	direct := NewTransport(nil)
	PublicOnly(direct, nil)
	if _, err := fetch(t, direct, site.URL); !errors.Is(err, ErrPrivateAddress) {
		t.Fatalf("напрямую на этот ПК: %v", err)
	}
	var hits atomic.Int32
	p := mustProxy(t, proxyServer(t, &hits).URL)
	viaProxy := NewTransport(p)
	PublicOnly(viaProxy, p)
	resp, err := fetch(t, viaProxy, site.URL) // соединение — только с прокси, дальше — его дело
	if err != nil || hits.Load() != 1 {
		t.Fatalf("через прокси в домашней сети: %v, запросов %d", err, hits.Load())
	}
	resp.Body.Close()
}
