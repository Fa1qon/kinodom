package discovery

import (
	"net"
	"net/netip"
	"strings"
	"testing"
)

const testUUID = "6b1f0d8e-1c2a-4e5f-9a7b-3c4d5e6f7a8b"

// M-SEARCH — запрос поиска с MAN "ssdp:discover"; регистр заголовков любой; NOTIFY и ответы — не запрос.
func TestParseSearch(t *testing.T) {
	cases := []struct {
		name, in, st string
		ok           bool
	}{
		{"обычный", "M-SEARCH * HTTP/1.1\r\nHOST: 239.255.255.250:1900\r\nMAN: \"ssdp:discover\"\r\nMX: 2\r\nST: urn:kinodom-ru:device:Kinodom:1\r\n\r\n", DeviceType, true},
		{"нижний регистр", "M-SEARCH * HTTP/1.1\r\nhost: 239.255.255.250:1900\r\nman: \"ssdp:discover\"\r\nst: ssdp:all\r\n\r\n", "ssdp:all", true},
		{"без MAN", "M-SEARCH * HTTP/1.1\r\nST: ssdp:all\r\n\r\n", "", false},
		{"без ST", "M-SEARCH * HTTP/1.1\r\nMAN: \"ssdp:discover\"\r\n\r\n", "", false},
		{"NOTIFY", "NOTIFY * HTTP/1.1\r\nNT: upnp:rootdevice\r\nNTS: ssdp:alive\r\n\r\n", "", false},
		{"ответ", "HTTP/1.1 200 OK\r\nST: ssdp:all\r\n\r\n", "", false},
		{"мусор", "\x00\x01garbage", "", false},
	}
	for _, c := range cases {
		st, ok := ParseSearch([]byte(c.in))
		if st != c.st || ok != c.ok {
			t.Errorf("%s: %q %v, нужно %q %v", c.name, st, ok, c.st, c.ok)
		}
	}
}

func TestMatches(t *testing.T) {
	for st, want := range map[string]bool{
		"ssdp:all": true, "upnp:rootdevice": true, DeviceType: true, "uuid:" + testUUID: true,
		"urn:schemas-upnp-org:device:MediaServer:1": false, "uuid:other": false, "": false,
	} {
		if got := Matches(st, testUUID); got != want {
			t.Errorf("%q: %v", st, got)
		}
	}
}

// Ответ на поиск: ST и USN по запросу, LOCATION — описание на порту пульта.
func TestReplyFormat(t *testing.T) {
	r := string(Reply(DeviceType, "http://192.168.0.10:8090/upnp/desc.xml", testUUID, "Kinodom/0.11.0"))
	for _, want := range []string{
		"HTTP/1.1 200 OK\r\n", "CACHE-CONTROL: max-age=1800\r\n", "EXT:\r\n",
		"LOCATION: http://192.168.0.10:8090/upnp/desc.xml\r\n", "ST: " + DeviceType + "\r\n",
		"USN: uuid:" + testUUID + "::" + DeviceType + "\r\n", "SERVER: Kinodom/0.11.0\r\n",
	} {
		if !strings.Contains(r, want) {
			t.Errorf("в ответе нет %q:\n%s", want, r)
		}
	}
	if !strings.HasSuffix(r, "\r\n\r\n") {
		t.Errorf("ответ не закончен пустой строкой: %q", r)
	}
	if all := string(Reply("ssdp:all", "http://x/upnp/desc.xml", testUUID, "K")); !strings.Contains(all, "ST: "+DeviceType+"\r\n") {
		t.Errorf("на ssdp:all — ответ про своё устройство:\n%s", all)
	}
	if root := string(Reply("upnp:rootdevice", "http://x/upnp/desc.xml", testUUID, "K")); !strings.Contains(root, "USN: uuid:"+testUUID+"::upnp:rootdevice\r\n") {
		t.Errorf("rootdevice:\n%s", root)
	}
}

// Review Focus 1: LOCATION — адрес интерфейса той же подсети, что отправитель (не VPN); отправитель из чужой
// подсети — без ответа.
func TestReplyLocationPerInterface(t *testing.T) {
	ifaces := []Iface{
		{Index: 3, Name: "Wi-Fi", Prefix: netip.MustParsePrefix("192.168.0.10/24")},
		{Index: 7, Name: "VirtualBox", Prefix: netip.MustParsePrefix("192.168.56.1/24")},
	}
	loc, ok := LocationFor(netip.MustParseAddr("192.168.0.50"), ifaces, 8090)
	if !ok || loc != "http://192.168.0.10:8090/upnp/desc.xml" {
		t.Fatalf("из домашней сети: %q %v", loc, ok)
	}
	if loc, ok := LocationFor(netip.MustParseAddr("192.168.56.101"), ifaces, 8090); !ok || loc != "http://192.168.56.1:8090/upnp/desc.xml" {
		t.Fatalf("из второй сети: %q %v", loc, ok)
	}
	if loc, ok := LocationFor(netip.MustParseAddr("10.8.0.5"), ifaces, 8090); ok {
		t.Fatalf("из чужой подсети (VPN) — ответа нет, а есть %q", loc)
	}
}

// Домашние интерфейсы: включён, многоадресная рассылка, не «точка-точка» (VPN), не loopback, частный IPv4.
func TestHomeIfaces(t *testing.T) {
	p := netip.MustParsePrefix
	in := []IfaceInfo{
		{Index: 1, Name: "Loopback", Flags: net.FlagUp | net.FlagLoopback | net.FlagMulticast, Addrs: []netip.Prefix{p("127.0.0.1/8")}},
		{Index: 3, Name: "Wi-Fi", Flags: net.FlagUp | net.FlagBroadcast | net.FlagMulticast, Addrs: []netip.Prefix{p("fe80::1/64"), p("192.168.0.10/24")}},
		{Index: 4, Name: "AmneziaVPN", Flags: net.FlagUp, Addrs: []netip.Prefix{p("10.8.0.2/24")}},
		{Index: 5, Name: "PPP", Flags: net.FlagUp | net.FlagPointToPoint | net.FlagMulticast, Addrs: []netip.Prefix{p("10.9.0.2/32")}},
		{Index: 6, Name: "Выключен", Flags: net.FlagBroadcast | net.FlagMulticast, Addrs: []netip.Prefix{p("192.168.1.5/24")}},
		{Index: 8, Name: "Белый адрес", Flags: net.FlagUp | net.FlagMulticast, Addrs: []netip.Prefix{p("8.8.4.4/24")}},
		{Index: 9, Name: "Ethernet", Flags: net.FlagUp | net.FlagBroadcast | net.FlagMulticast, Addrs: []netip.Prefix{p("10.0.0.20/8")}},
	}
	got := HomeIfaces(in)
	var names []string
	for _, i := range got {
		names = append(names, i.Name+"="+i.Prefix.String())
	}
	if want := "Wi-Fi=192.168.0.10/24 Ethernet=10.0.0.20/8"; strings.Join(names, " ") != want {
		t.Fatalf("домашние: %v, нужно %s", names, want)
	}
}
