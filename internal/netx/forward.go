package netx

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"golang.org/x/net/proxy"
)

// Forwarder — локальный переходник к прокси с логином и паролем. Edge не умеет передавать пароль
// прокси (у флага --proxy-server нет логина), поэтому на время прохода Cloudflare Edge ходит сюда,
// а переходник — дальше, к настоящему прокси, с логином и паролем (спека этапа 7, раздел 5.2).
// Слушает только 127.0.0.1: с других компьютеров до него не достать.
type Forwarder struct {
	upstream *url.URL
	ln       net.Listener
	srv      *http.Server
	tr       *http.Transport
}

// Forward поднимает переходник к upstream (http:// или socks5://, с логином и паролем в адресе).
func Forward(upstream *url.URL) (*Forwarder, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("переходник к прокси: %w", err)
	}
	f := &Forwarder{upstream: upstream, ln: ln, tr: NewTransport(&Proxy{u: upstream})}
	f.srv = &http.Server{Handler: f, ReadHeaderTimeout: 30 * time.Second}
	go f.srv.Serve(ln)
	return f, nil
}

// Addr — «127.0.0.1:порт» для флага --proxy-server=http://….
func (f *Forwarder) Addr() string { return f.ln.Addr().String() }

// Close останавливает переходник. Туннели, которые ещё открыты, закрывает сам Edge, когда выходит.
func (f *Forwarder) Close() error {
	f.tr.CloseIdleConnections()
	return f.srv.Close()
}

func (f *Forwarder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		f.tunnel(w, r)
		return
	}
	if !r.URL.IsAbs() {
		http.Error(w, "переходник к прокси: нужен полный адрес", http.StatusBadRequest)
		return
	}
	out := r.Clone(r.Context())
	out.RequestURI = ""
	out.Header.Del("Proxy-Connection")
	out.Header.Del("Proxy-Authorization")
	resp, err := f.tr.RoundTrip(out)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusProxyAuthRequired {
		// Edge на 407 спросил бы пароль у человека, а в скрытом окне его некому ввести.
		http.Error(w, errProxyAuth.Error(), http.StatusBadGateway)
		return
	}
	for k, v := range resp.Header {
		w.Header()[k] = v
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

// tunnel — HTTPS: соединение с сайтом через прокси, дальше байты туда и обратно как есть.
func (f *Forwarder) tunnel(w http.ResponseWriter, r *http.Request) {
	up, err := dialVia(r.Context(), f.upstream, r.Host)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		up.Close()
		http.Error(w, "переходник к прокси: соединение не перехватить", http.StatusInternalServerError)
		return
	}
	conn, buf, err := hj.Hijack()
	if err != nil {
		up.Close()
		return
	}
	if _, err := io.WriteString(conn, "HTTP/1.1 200 Connection established\r\n\r\n"); err != nil {
		conn.Close()
		up.Close()
		return
	}
	go func() {
		io.Copy(up, buf) // buf читает из conn, включая то, что клиент успел прислать сразу
		up.Close()
	}()
	io.Copy(conn, up)
	conn.Close()
}

// dialVia — соединение с target («хост:порт») через прокси upstream: HTTP — командой CONNECT с
// заголовком Proxy-Authorization, SOCKS5 — входом по имени и паролю.
func dialVia(ctx context.Context, upstream *url.URL, target string) (net.Conn, error) {
	d := &net.Dialer{Timeout: 30 * time.Second}
	if upstream.Scheme == "socks5" {
		var auth *proxy.Auth
		if upstream.User != nil {
			pw, _ := upstream.User.Password()
			auth = &proxy.Auth{User: upstream.User.Username(), Password: pw}
		}
		sd, err := proxy.SOCKS5("tcp", upstream.Host, auth, d)
		if err != nil {
			return nil, err
		}
		conn, err := sd.(proxy.ContextDialer).DialContext(ctx, "tcp", target)
		if refusal := proxyRefusal(err); refusal != nil {
			return nil, refusal
		}
		return conn, err
	}
	c, err := d.DialContext(ctx, "tcp", upstream.Host)
	if err != nil {
		return nil, fmt.Errorf("%w (%s): %w", ErrProxyDown, upstream.Host, err)
	}
	req := &http.Request{Method: http.MethodConnect, URL: &url.URL{Opaque: target}, Host: target, Header: http.Header{}}
	if upstream.User != nil {
		pw, _ := upstream.User.Password()
		req.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(upstream.User.Username()+":"+pw)))
	}
	if err := req.Write(c); err != nil {
		c.Close()
		return nil, err
	}
	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		c.Close()
		return nil, err
	}
	resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusProxyAuthRequired:
		c.Close()
		return nil, errProxyAuth
	case resp.StatusCode != http.StatusOK:
		c.Close()
		return nil, fmt.Errorf("прокси не открыл соединение с %s: %s", target, resp.Status)
	}
	if br.Buffered() > 0 {
		return &bufferedConn{Conn: c, r: br}, nil
	}
	return c, nil
}

// bufferedConn — соединение, часть ответа которого уже прочитана в буфер.
type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (b *bufferedConn) Read(p []byte) (int, error) { return b.r.Read(p) }
