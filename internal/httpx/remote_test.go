package httpx

import (
	"net"
	"net/http/httptest"
	"testing"
	"time"
)

// setOwn подменяет адреса интерфейсов ПК на время теста.
func setOwn(t *testing.T, cidrs ...string) {
	t.Helper()
	var addrs []net.Addr
	for _, c := range cidrs {
		ip, n, err := net.ParseCIDR(c)
		if err != nil {
			t.Fatal(err)
		}
		n.IP = ip
		addrs = append(addrs, n)
	}
	was := interfaceAddrs
	interfaceAddrs = func() ([]net.Addr, error) { return addrs, nil }
	resetOwn()
	t.Cleanup(func() {
		interfaceAddrs = was
		resetOwn()
	})
}

// Этот ПК — loopback и его собственные адреса; домашняя сеть — ещё частные и локальные для сегмента
// адреса (спека этапа 7, раздел 10.1).
func TestFromThisPCAndHome(t *testing.T) {
	setOwn(t, "127.0.0.1/8", "192.168.0.26/24", "2a02:6b8::26/64")
	cases := []struct {
		remote   string
		pc, home bool
	}{
		{"127.0.0.1:5000", true, true},
		{"[::1]:5000", true, true},
		{"192.168.0.26:5000", true, true},   // пульт на этом ПК, открытый по сетевому адресу
		{"[2a02:6b8::26]:5000", true, true}, // свой публичный IPv6
		{"192.168.0.50:5000", false, true},  // телефон в Wi-Fi
		{"10.8.0.2:5000", false, true},
		{"172.20.1.1:5000", false, true},
		{"169.254.10.1:5000", false, true},
		{"[fe80::1%eth0]:5000", false, true},
		{"[fd12::5]:5000", false, true},
		{"[::ffff:192.168.0.50]:5000", false, true},
		{"172.32.0.1:5000", false, false},
		{"8.8.8.8:5000", false, false},
		{"[2001:db8::1]:5000", false, false},
		{"не адрес", false, false},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = c.remote
		if got := FromThisPC(r); got != c.pc {
			t.Errorf("%s: этот ПК = %v, нужно %v", c.remote, got, c.pc)
		}
		if got := FromHome(r); got != c.home {
			t.Errorf("%s: домашняя сеть = %v, нужно %v", c.remote, got, c.home)
		}
	}
}

// Устройство — адрес запроса; все адреса этого ПК — одно устройство «pc», IPv4 внутри IPv6 — как
// IPv4 (спека этапа 8, раздел 4).
func TestDevice(t *testing.T) {
	setOwn(t, "127.0.0.1/8", "192.168.0.26/24")
	cases := map[string]string{
		"127.0.0.1:5000":             "pc",
		"[::1]:5000":                 "pc",
		"192.168.0.26:5000":          "pc",
		"192.168.0.50:5000":          "192.168.0.50",
		"[::ffff:192.168.0.50]:6000": "192.168.0.50",
		"[fe80::1%eth0]:5000":        "fe80::1",
		"не адрес":                   "",
	}
	for remote, want := range cases {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = remote
		if got := Device(r); got != want {
			t.Errorf("%s: устройство %q, нужно %q", remote, got, want)
		}
	}
}

// Адреса ПК для телефонов и ТВ (экран «Готово» мастера, этап 11a): только частные IPv4, сначала
// 192.168.*, без loopback, локальных для сегмента и внешних.
func TestHomeAddresses(t *testing.T) {
	setOwn(t, "127.0.0.1/8", "10.8.0.2/24", "192.168.1.5/24", "169.254.3.4/16", "fe80::1/64", "8.8.4.4/32", "172.20.0.9/16")
	got := HomeAddresses()
	want := []string{"192.168.1.5", "10.8.0.2", "172.20.0.9"}
	if len(got) != len(want) {
		t.Fatalf("адреса %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("адреса %v, ждали %v", got, want)
		}
	}
}

// Адрес ПК сменился (DHCP, другой Wi-Fi) — новый адрес — «этот ПК» без перезапуска, старый — уже нет:
// промах перечитывает список (не чаще раза в 10 с), а список старше минуты перечитывается сам
// (хвост 7b, спека этапа 11a, раздел 8).
func TestOwnAddressChange(t *testing.T) {
	setOwn(t, "127.0.0.1/8", "192.168.0.26/24")
	clock := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	was := now
	now = func() time.Time { return clock }
	t.Cleanup(func() { now = was })
	pc := func(addr string) bool {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = addr + ":5000"
		return FromThisPC(r)
	}
	if !pc("192.168.0.26") {
		t.Fatal("свой адрес не узнан")
	}
	setAddrs(t, "127.0.0.1/8", "192.168.0.40/24")
	clock = clock.Add(11 * time.Second)
	if !pc("192.168.0.40") {
		t.Fatal("новый адрес ПК не узнан без перезапуска")
	}
	if pc("192.168.0.26") {
		t.Fatal("старый адрес ПК всё ещё «этот ПК»")
	}
	// Без промахов: адрес ушёл — через минуту он уже не свой.
	setAddrs(t, "127.0.0.1/8", "192.168.0.50/24")
	clock = clock.Add(2 * time.Second)
	if !pc("192.168.0.40") {
		t.Fatal("список перечитан раньше минуты")
	}
	clock = clock.Add(time.Minute)
	if pc("192.168.0.40") {
		t.Fatal("ушедший адрес — «этот ПК» дольше минуты")
	}
	if !IsOwnIP(net.ParseIP("192.168.0.50")) || IsOwnIP(net.ParseIP("192.168.0.99")) {
		t.Fatal("IsOwnIP")
	}
}

// setAddrs меняет адреса интерфейсов, не сбрасывая кэш: как настоящая смена адреса.
func setAddrs(t *testing.T, cidrs ...string) {
	t.Helper()
	var addrs []net.Addr
	for _, c := range cidrs {
		ip, n, err := net.ParseCIDR(c)
		if err != nil {
			t.Fatal(err)
		}
		n.IP = ip
		addrs = append(addrs, n)
	}
	interfaceAddrs = func() ([]net.Addr, error) { return addrs, nil }
}
