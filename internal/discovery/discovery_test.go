package discovery

import (
	"bufio"
	"context"
	"encoding/xml"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/ipv4"
)

// Описание устройства: тип, имя ПК (с экранированием), UDN, presentationURL — адрес, по которому описание
// спросили.
func TestDescXML(t *testing.T) {
	m := New(Options{APIPort: 8090, Name: `ПК <Семья> & "дом"`, UUID: testUUID})
	mux := http.NewServeMux()
	m.Register(muxRouter{mux})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/upnp/desc.xml", nil)
	req.Host = "192.168.0.10:8090"
	mux.ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/xml") {
		t.Fatalf("ответ %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	var d struct {
		Device struct {
			DeviceType      string `xml:"deviceType"`
			FriendlyName    string `xml:"friendlyName"`
			Manufacturer    string `xml:"manufacturer"`
			UDN             string `xml:"UDN"`
			PresentationURL string `xml:"presentationURL"`
		} `xml:"device"`
	}
	if err := xml.Unmarshal(rec.Body.Bytes(), &d); err != nil {
		t.Fatalf("не XML: %v\n%s", err, rec.Body.String())
	}
	got := d.Device
	if got.DeviceType != DeviceType || got.FriendlyName != `Kinodom на ПК <Семья> & "дом"` || got.Manufacturer != "Kinodom" ||
		got.UDN != "uuid:"+testUUID || got.PresentationURL != "http://192.168.0.10:8090/" {
		t.Fatalf("описание: %+v", got)
	}
}

type muxRouter struct{ *http.ServeMux }

func (m muxRouter) Handle(pattern, _ string, h http.Handler) { m.ServeMux.Handle(pattern, h) }

// Круг целиком: модуль слушает группу (порт теста) на домашнем интерфейсе этого ПК, M-SEARCH с этого же ПК
// получает ответ с LOCATION этого интерфейса (исследование, раздел 5: стандартный приём многоадресных
// пакетов в Go свои пакеты не видит — модуль включает их приём).
func TestSearchRoundTrip(t *testing.T) {
	ifaces := SystemIfaces()
	if len(ifaces) == 0 {
		t.Skip("на этом ПК нет интерфейса домашней сети")
	}
	home := ifaces[0]
	port := freeUDPPort(t)
	group := netip.AddrPortFrom(netip.MustParseAddr("239.255.255.250"), uint16(port))
	m := New(Options{APIPort: 8090, Name: "тест", UUID: testUUID, Ifaces: func() []Iface { return []Iface{home} }, Group: group,
		Log: slog.New(slog.DiscardHandler)})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- m.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("модуль не остановился")
		}
	})

	c, err := net.ListenPacket("udp4", home.Prefix.Addr().String()+":0")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	p := ipv4.NewPacketConn(c)
	ifi, err := net.InterfaceByIndex(home.Index)
	if err != nil {
		t.Fatal(err)
	}
	p.SetMulticastInterface(ifi)
	p.SetMulticastLoopback(true)
	search := []byte("M-SEARCH * HTTP/1.1\r\nHOST: 239.255.255.250:1900\r\nMAN: \"ssdp:discover\"\r\nMX: 1\r\nST: " + DeviceType + "\r\n\r\n")
	dst := net.UDPAddrFromAddrPort(group)
	want := "http://" + home.Prefix.Addr().String() + ":8090/upnp/desc.xml"
	deadline := time.Now().Add(5 * time.Second)
	buf := make([]byte, 2048)
	for time.Now().Before(deadline) {
		if _, err := c.WriteTo(search, dst); err != nil {
			t.Fatal(err)
		}
		c.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		n, _, err := c.ReadFrom(buf)
		if err != nil {
			continue // модуль мог ещё не подключиться к группе — спросить снова
		}
		resp, err := http.ReadResponse(bufio.NewReader(strings.NewReader(string(buf[:n]))), nil)
		if err != nil {
			t.Fatalf("ответ не HTTP: %v\n%s", err, buf[:n])
		}
		if loc := resp.Header.Get("Location"); loc != want || resp.Header.Get("St") != DeviceType {
			t.Fatalf("ответ: LOCATION %q ST %q, нужно %q", loc, resp.Header.Get("St"), want)
		}
		return
	}
	t.Fatal("на M-SEARCH с этого ПК ответа нет")
}

func freeUDPPort(t *testing.T) int {
	t.Helper()
	c, err := net.ListenPacket("udp4", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	return c.LocalAddr().(*net.UDPAddr).Port
}
