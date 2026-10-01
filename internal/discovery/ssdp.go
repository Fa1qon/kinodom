package discovery

import (
	"bufio"
	"bytes"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
)

// DeviceType — тип устройства Kinodom в SSDP: по нему приложение находит сервер (основная спека, раздел 12).
const DeviceType = "urn:kinodom-ru:device:Kinodom:1"

// descPath — описание устройства на порту пульта.
const descPath = "/upnp/desc.xml"

// ParseSearch — запрос поиска M-SEARCH с MAN "ssdp:discover": что ищут (ST). Остальное (NOTIFY, ответы,
// мусор) — не запрос.
func ParseSearch(b []byte) (st string, ok bool) {
	req, err := http.ReadRequest(bufio.NewReader(bytes.NewReader(b)))
	if err != nil || req.Method != "M-SEARCH" {
		return "", false
	}
	if strings.Trim(req.Header.Get("Man"), `"`) != "ssdp:discover" {
		return "", false
	}
	st = strings.TrimSpace(req.Header.Get("St"))
	return st, st != ""
}

// Matches — запрос про Kinodom: все устройства, корневые, тип Kinodom или этот сервер по UUID.
func Matches(st, uuid string) bool {
	switch st {
	case "ssdp:all", "upnp:rootdevice", DeviceType, "uuid:" + uuid:
		return true
	}
	return false
}

// usnFor — ST и USN ответа: на «все» и тип — тип Kinodom, на корневые — rootdevice, на UUID — он.
func usnFor(st, uuid string) (string, string) {
	switch st {
	case "upnp:rootdevice":
		return st, "uuid:" + uuid + "::upnp:rootdevice"
	case "uuid:" + uuid:
		return st, st
	}
	return DeviceType, "uuid:" + uuid + "::" + DeviceType
}

// Reply — ответ на поиск (unicast отправителю).
func Reply(st, location, uuid, server string) []byte {
	st, usn := usnFor(st, uuid)
	return []byte("HTTP/1.1 200 OK\r\n" +
		"CACHE-CONTROL: max-age=1800\r\n" +
		"EXT:\r\n" +
		"LOCATION: " + location + "\r\n" +
		"SERVER: " + server + "\r\n" +
		"ST: " + st + "\r\n" +
		"USN: " + usn + "\r\n\r\n")
}

// notify — объявление в группу: nts — ssdp:alive или ssdp:byebye.
func notify(nts, location, uuid, server string, group netip.AddrPort) []byte {
	head := "NOTIFY * HTTP/1.1\r\n" +
		"HOST: " + group.String() + "\r\n" +
		"NT: " + DeviceType + "\r\n" +
		"NTS: " + nts + "\r\n" +
		"USN: uuid:" + uuid + "::" + DeviceType + "\r\n"
	if nts == "ssdp:alive" {
		head += "CACHE-CONTROL: max-age=1800\r\n" +
			"LOCATION: " + location + "\r\n" +
			"SERVER: " + server + "\r\n"
	}
	return []byte(head + "\r\n")
}

// LocationFor — адрес описания для отправителя: интерфейс той же подсети (не VPN — ответ должен вести туда,
// откуда телевизор достучится). Отправитель из чужой подсети — ответа нет.
func LocationFor(from netip.Addr, ifaces []Iface, port int) (string, bool) {
	from = from.Unmap()
	for _, i := range ifaces {
		if i.Prefix.Contains(from) {
			return locationOn(i, port), true
		}
	}
	return "", false
}

func locationOn(i Iface, port int) string {
	return "http://" + i.Prefix.Addr().String() + ":" + strconv.Itoa(port) + descPath
}

// Iface — интерфейс домашней сети: индекс (для группы), имя, адрес с маской.
type Iface struct {
	Index  int
	Name   string
	Prefix netip.Prefix
}

// IfaceInfo — интерфейс, как его видит система.
type IfaceInfo struct {
	Index int
	Name  string
	Flags net.Flags
	Addrs []netip.Prefix
}

// HomeIfaces — интерфейсы домашней сети: включён, умеет многоадресную рассылку, не «точка-точка» (VPN), не
// loopback, адрес — частный IPv4. Адаптер VPN (WireGuard у AmneziaVPN) многоадресную рассылку не объявляет.
func HomeIfaces(ifs []IfaceInfo) []Iface {
	var out []Iface
	for _, i := range ifs {
		if i.Flags&net.FlagUp == 0 || i.Flags&net.FlagMulticast == 0 || i.Flags&(net.FlagLoopback|net.FlagPointToPoint) != 0 {
			continue
		}
		for _, a := range i.Addrs {
			if ip := a.Addr(); ip.Is4() && ip.IsPrivate() {
				out = append(out, Iface{Index: i.Index, Name: i.Name, Prefix: a})
				break
			}
		}
	}
	return out
}

// SystemIfaces — интерфейсы домашней сети этого ПК сейчас.
func SystemIfaces() []Iface {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil
	}
	infos := make([]IfaceInfo, 0, len(ifs))
	for _, i := range ifs {
		info := IfaceInfo{Index: i.Index, Name: i.Name, Flags: i.Flags}
		addrs, err := i.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok {
				if ip, ok := netip.AddrFromSlice(n.IP); ok {
					ones, _ := n.Mask.Size()
					info.Addrs = append(info.Addrs, netip.PrefixFrom(ip.Unmap(), ones))
				}
			}
		}
		infos = append(infos, info)
	}
	return HomeIfaces(infos)
}
