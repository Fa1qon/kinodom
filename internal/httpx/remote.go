package httpx

import (
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
)

// interfaceAddrs — адреса сетевых интерфейсов ПК (тесты подменяют).
var interfaceAddrs = net.InterfaceAddrs

// own — адреса самого ПК. Если адреса запроса среди них нет, список перечитывается, но не чаще раза
// в 10 с: адрес мог смениться (DHCP, другой Wi-Fi).
var own struct {
	mu    sync.Mutex
	addrs map[string]bool
	at    time.Time
}

func resetOwn() {
	own.mu.Lock()
	own.addrs, own.at = nil, time.Time{}
	own.mu.Unlock()
}

func isOwn(ip net.IP) bool {
	key := ip.String()
	own.mu.Lock()
	defer own.mu.Unlock()
	if own.addrs[key] || (own.addrs != nil && time.Since(own.at) < 10*time.Second) {
		return own.addrs[key]
	}
	own.addrs, own.at = map[string]bool{}, time.Now()
	if as, err := interfaceAddrs(); err == nil {
		for _, a := range as {
			if n, ok := a.(*net.IPNet); ok {
				own.addrs[unmap(n.IP).String()] = true
			}
		}
	}
	return own.addrs[key]
}

// unmap — адрес IPv4 внутри IPv6 (::ffff:192.168.0.5) как IPv4.
func unmap(ip net.IP) net.IP {
	if ip4 := ip.To4(); ip4 != nil {
		return ip4
	}
	return ip
}

// remoteIP — адрес, с которого пришёл запрос; nil — не разобрать.
func remoteIP(r *http.Request) net.IP {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return nil
	}
	if i := strings.IndexByte(host, '%'); i >= 0 { // fe80::1%eth0 — зона интерфейса не часть адреса
		host = host[:i]
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return nil
	}
	return unmap(ip)
}

// FromThisPC — запрос с этого ПК: loopback или один из адресов самого ПК, то есть и пульт, открытый на
// ПК по адресу в сети (спека этапа 7, раздел 10.1). Только такому запросу отдаётся ссылка kinodom://.
func FromThisPC(r *http.Request) bool {
	ip := remoteIP(r)
	return ip != nil && (ip.IsLoopback() || isOwn(ip))
}

// FromHome — запрос из домашней сети: этот ПК, частные адреса (10/8, 172.16/12, 192.168/16, fc00::/7) и
// локальные для сегмента (169.254/16, fe80::/10). Отсюда можно менять настройки и удалять загрузки
// (решение заказчика, спека этапа 7, раздел 10.1).
func FromHome(r *http.Request) bool {
	ip := remoteIP(r)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || isOwn(ip))
}

// DevicePC — устройство «этот ПК»: loopback и все его адреса в сети.
const DevicePC = "pc"

// Device — устройство, с которого пришёл запрос: у каждого своё избранное и своя история просмотров
// (спека этапа 8, раздел 4). Это адрес без порта; все адреса этого ПК — одно устройство DevicePC.
// "" — адрес не разобрать.
func Device(r *http.Request) string {
	ip := remoteIP(r)
	switch {
	case ip == nil:
		return ""
	case ip.IsLoopback() || isOwn(ip):
		return DevicePC
	}
	return ip.String()
}

// HomeAddresses — адреса этого ПК в домашней сети для телефонов и ТВ: частные IPv4, сначала
// 192.168.* (обычная домашняя сеть), затем 10.* и 172.16–31.*.
func HomeAddresses() []string {
	as, err := interfaceAddrs()
	if err != nil {
		return nil
	}
	rank := func(ip net.IP) int {
		switch ip[0] {
		case 192:
			return 0
		case 10:
			return 1
		}
		return 2
	}
	var ips []net.IP
	for _, a := range as {
		n, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		if ip := n.IP.To4(); ip != nil && ip.IsPrivate() {
			ips = append(ips, ip)
		}
	}
	slices.SortStableFunc(ips, func(a, b net.IP) int { return rank(a) - rank(b) })
	out := make([]string, len(ips))
	for i, ip := range ips {
		out[i] = ip.String()
	}
	return out
}
