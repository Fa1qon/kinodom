package netx

import (
	"encoding/base64"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

// authHTTPProxy — HTTP-прокси, который пускает только с логином user и паролем secret: обычные
// запросы пересылает, на CONNECT открывает туннель. hits — сколько запросов прошло с паролем.
func authHTTPProxy(t *testing.T, hits *atomic.Int32) *url.URL {
	t.Helper()
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("user:secret"))
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Proxy-Authorization") != want {
			w.WriteHeader(http.StatusProxyAuthRequired)
			return
		}
		hits.Add(1)
		if r.Method == http.MethodConnect {
			up, err := net.Dial("tcp", r.Host)
			if err != nil {
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			conn, buf, err := w.(http.Hijacker).Hijack()
			if err != nil {
				up.Close()
				return
			}
			io.WriteString(conn, "HTTP/1.1 200 Connection established\r\n\r\n")
			go func() { io.Copy(up, buf); up.Close() }()
			io.Copy(conn, up)
			conn.Close()
			return
		}
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
	u, _ := url.Parse(s.URL)
	return u
}

// authSOCKS5 — SOCKS5-прокси (RFC 1928, вход по RFC 1929), который пускает только user/secret.
func authSOCKS5(t *testing.T, hits *atomic.Int32) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go serveSOCKS5(c, hits)
		}
	}()
	return ln.Addr().String()
}

func serveSOCKS5(c net.Conn, hits *atomic.Int32) {
	defer c.Close()
	hdr := make([]byte, 2)
	if _, err := io.ReadFull(c, hdr); err != nil {
		return
	}
	io.ReadFull(c, make([]byte, hdr[1]))
	c.Write([]byte{5, 2}) // вход по имени и паролю
	var ver, n [1]byte
	io.ReadFull(c, ver[:])
	io.ReadFull(c, n[:])
	user := make([]byte, n[0])
	io.ReadFull(c, user)
	io.ReadFull(c, n[:])
	pass := make([]byte, n[0])
	io.ReadFull(c, pass)
	if string(user) != "user" || string(pass) != "secret" {
		c.Write([]byte{1, 1})
		return
	}
	c.Write([]byte{1, 0})
	req := make([]byte, 4) // версия, команда, 0, тип адреса
	if _, err := io.ReadFull(c, req); err != nil {
		return
	}
	var host string
	switch req[3] {
	case 1:
		ip := make([]byte, 4)
		io.ReadFull(c, ip)
		host = net.IP(ip).String()
	case 3:
		io.ReadFull(c, n[:])
		name := make([]byte, n[0])
		io.ReadFull(c, name)
		host = string(name)
	default:
		return
	}
	pb := make([]byte, 2)
	io.ReadFull(c, pb)
	up, err := net.Dial("tcp", net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(pb)))))
	if err != nil {
		c.Write([]byte{5, 5, 0, 1, 0, 0, 0, 0, 0, 0})
		return
	}
	defer up.Close()
	hits.Add(1)
	c.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0})
	go io.Copy(up, c)
	io.Copy(c, up)
}

// clientVia — HTTP-клиент, который ходит через переходник без пароля, как Edge.
func clientVia(t *testing.T, f *Forwarder, tlsSite *httptest.Server) *http.Client {
	t.Helper()
	u, _ := url.Parse("http://" + f.Addr())
	tr := &http.Transport{Proxy: http.ProxyURL(u)}
	if tlsSite != nil {
		tr.TLSClientConfig = tlsSite.Client().Transport.(*http.Transport).TLSClientConfig
	}
	t.Cleanup(tr.CloseIdleConnections)
	return &http.Client{Transport: tr}
}

func get(t *testing.T, c *http.Client, u string) (int, string) {
	t.Helper()
	resp, err := c.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// Edge не умеет пароль прокси: переходник на 127.0.0.1 без пароля доводит и HTTPS (CONNECT), и
// обычные запросы до сайта через прокси с логином и паролем — HTTP и SOCKS5 (спека этапа 7, 5.2).
func TestForwarderAddsProxyPassword(t *testing.T) {
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "сайт") }))
	defer plain.Close()
	secure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "защищённый сайт") }))
	defer secure.Close()
	var httpHits, socksHits atomic.Int32
	hu := authHTTPProxy(t, &httpHits)
	for name, up := range map[string]string{
		"HTTP":   "http://user:secret@" + hu.Host,
		"SOCKS5": "socks5://user:secret@" + authSOCKS5(t, &socksHits),
	} {
		t.Run(name, func(t *testing.T) {
			u, _ := url.Parse(up)
			f, err := Forward(u)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			if !strings.HasPrefix(f.Addr(), "127.0.0.1:") {
				t.Fatalf("переходник слушает %s, а не только этот ПК", f.Addr())
			}
			if code, body := get(t, clientVia(t, f, secure), secure.URL); code != 200 || body != "защищённый сайт" {
				t.Fatalf("HTTPS через переходник: %d %q", code, body)
			}
			if code, body := get(t, clientVia(t, f, nil), plain.URL); code != 200 || body != "сайт" {
				t.Fatalf("HTTP через переходник: %d %q", code, body)
			}
		})
	}
	if httpHits.Load() < 2 || socksHits.Load() < 2 {
		t.Fatalf("прокси с паролем пропустили: HTTP %d, SOCKS5 %d", httpHits.Load(), socksHits.Load())
	}
}

// Неверный пароль — ответ переходника 502 с понятной причиной, а не зависание.
func TestForwarderWrongPasswordIsExplained(t *testing.T) {
	var hits atomic.Int32
	hu := authHTTPProxy(t, &hits)
	u, _ := url.Parse("http://user:wrong@" + hu.Host)
	f, err := Forward(u)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	secure := httptest.NewTLSServer(http.NotFoundHandler())
	defer secure.Close()
	resp, err := clientVia(t, f, secure).Get(secure.URL)
	if err == nil {
		resp.Body.Close()
		t.Fatal("HTTPS прошёл с неверным паролем")
	}
	code, body := get(t, clientVia(t, f, nil), "http://"+closedAddr(t)+"/")
	if code != http.StatusBadGateway || !strings.Contains(body, "логин или пароль") {
		t.Fatalf("обычный запрос с неверным паролем: %d %q", code, body)
	}
}
