package netx

import (
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// closedAddr — адрес на localhost, где никто не слушает.
func closedAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

func fetch(t *testing.T, tr *http.Transport, u string) (*http.Response, error) {
	t.Helper()
	resp, err := (&http.Client{Transport: tr, Timeout: 10 * time.Second}).Get(u)
	if err == nil {
		t.Cleanup(func() { resp.Body.Close() })
	}
	return resp, err
}

// proxyServer — простейший HTTP-прокси: запрос к http:// приходит к нему с полным адресом.
func proxyServer(t *testing.T, hits *atomic.Int32) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		out, _ := http.NewRequestWithContext(r.Context(), r.Method, r.URL.String(), nil)
		resp, err := http.DefaultTransport.RoundTrip(out)
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
	}))
	t.Cleanup(s.Close)
	return s
}

func TestTransportGoesThroughHTTPProxy(t *testing.T) {
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "сайт")
	}))
	defer site.Close()
	var hits atomic.Int32
	tr, err := NewTransport(proxyServer(t, &hits).URL)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := fetch(t, tr, site.URL)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "сайт" || hits.Load() != 1 {
		t.Fatalf("ответ %q, через прокси прошло %d запросов", body, hits.Load())
	}
}

func TestProxyDownIsReportedAsProxyDown(t *testing.T) {
	for _, scheme := range []string{"http", "socks5"} {
		tr, err := NewTransport(scheme + "://" + closedAddr(t))
		if err != nil {
			t.Fatal(err)
		}
		_, err = fetch(t, tr, "http://example.invalid/")
		if !errors.Is(err, ErrProxyDown) {
			t.Errorf("%s: ожидалась ErrProxyDown, получено %v", scheme, err)
		}
	}
}

// Сайт за работающим прокси не отвечает — это не «прокси не отвечает».
func TestSiteDownBehindProxyIsNotProxyDown(t *testing.T) {
	var hits atomic.Int32
	tr, err := NewTransport(proxyServer(t, &hits).URL)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := fetch(t, tr, "http://"+closedAddr(t)+"/")
	if err != nil || resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("ожидался ответ прокси 502, получено %v %v", resp, err)
	}
}

// Пустой прокси — напрямую; переменные окружения не читаются (у службы их нет).
func TestDirectTransportIgnoresEnvProxy(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://"+closedAddr(t))
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer site.Close()
	tr, err := NewTransport("")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fetch(t, tr, site.URL); err != nil {
		t.Fatal(err)
	}
}
