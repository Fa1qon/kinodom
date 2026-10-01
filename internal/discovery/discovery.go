// Package discovery — модуль «discovery»: сервер объявляет себя в домашней сети по SSDP, чтобы приложение
// для ТВ и телефона находило его само (основная спека, раздел 12; спека этапа 13, раздел 5.1). Только
// интерфейсы домашней сети, приём своих пакетов (исследование, раздел 5), описание — /upnp/desc.xml на порту
// пульта.
package discovery

import (
	"context"
	"encoding/xml"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"time"

	"golang.org/x/net/ipv4"

	"kinodom/internal/supervisor"
)

// Расписание (спека этапа 13, раздел 5.1).
const (
	notifyEvery = 15 * time.Minute // объявление «я здесь»
	rescanEvery = time.Minute      // Wi-Fi и VPN появляются позже службы
)

// DefaultGroup — группа SSDP.
var DefaultGroup = netip.MustParseAddrPort("239.255.255.250:1900")

type Options struct {
	APIPort int            // порт пульта: LOCATION и presentationURL
	Name    string         // имя ПК: friendlyName «Kinodom на <имя>»
	UUID    string         // постоянный (настройка discovery.uuid)
	Server  string         // SERVER в ответах; "" — «Kinodom»
	Ifaces  func() []Iface // интерфейсы домашней сети; nil — SystemIfaces
	Group   netip.AddrPort // нуль — DefaultGroup; тесты — свой порт
	Log     *slog.Logger
}

// Module — модуль «discovery».
type Module struct {
	o Options

	mu     sync.Mutex
	joined map[int]Iface // индекс интерфейса → интерфейс, в группе которого слушаем
}

// Router — то, что модулю нужно от HTTP-сервера; api.Server ему соответствует.
type Router interface {
	Handle(pattern, module string, h http.Handler)
}

func New(o Options) *Module {
	if o.Ifaces == nil {
		o.Ifaces = SystemIfaces
	}
	if !o.Group.IsValid() {
		o.Group = DefaultGroup
	}
	if o.Server == "" {
		o.Server = "Kinodom"
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	return &Module{o: o, joined: map[int]Iface{}}
}

func (m *Module) Name() string { return "discovery" }

// Register — описание устройства: GET /upnp/desc.xml.
func (m *Module) Register(r Router) {
	r.Handle("GET "+descPath, m.Name(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/xml; charset=utf-8")
		w.Write(descXML(m.o.Name, m.o.UUID, "http://"+r.Host+"/"))
	}))
}

// descXML — описание устройства UPnP: тип, имя, UDN и адрес пульта.
func descXML(name, uuid, base string) []byte {
	type device struct {
		DeviceType      string `xml:"deviceType"`
		FriendlyName    string `xml:"friendlyName"`
		Manufacturer    string `xml:"manufacturer"`
		ModelName       string `xml:"modelName"`
		UDN             string `xml:"UDN"`
		PresentationURL string `xml:"presentationURL"`
	}
	type spec struct {
		Major int `xml:"major"`
		Minor int `xml:"minor"`
	}
	type root struct {
		XMLName xml.Name `xml:"urn:schemas-upnp-org:device-1-0 root"`
		Spec    spec     `xml:"specVersion"`
		Device  device   `xml:"device"`
	}
	b, _ := xml.MarshalIndent(root{Spec: spec{1, 0}, Device: device{DeviceType: DeviceType, FriendlyName: "Kinodom на " + name,
		Manufacturer: "Kinodom", ModelName: "Kinodom", UDN: "uuid:" + uuid, PresentationURL: base}}, "", " ")
	return append([]byte(xml.Header), b...)
}

// Run — ждёт интерфейс домашней сети, слушает группу на каждом, отвечает на поиск, объявляет себя при старте
// и раз в 15 минут, пересматривает интерфейсы раз в минуту; при остановке — «ухожу» (byebye).
func (m *Module) Run(ctx context.Context) error {
	supervisor.Ready(ctx) // модуль работает и тогда, когда ждёт домашнюю сеть
	var p *ipv4.PacketConn
	tick := time.NewTicker(rescanEvery)
	defer tick.Stop()
	for p == nil {
		if ifs := m.o.Ifaces(); len(ifs) > 0 {
			var err error
			if p, err = m.listen(ifs[0]); err != nil {
				m.o.Log.Warn("обнаружение: группа SSDP недоступна, повтор через минуту", "err", err)
				p = nil
			}
		}
		if p == nil {
			select {
			case <-ctx.Done():
				return nil
			case <-tick.C:
			}
		}
	}
	defer p.Close()
	m.rescan(p)
	m.notifyAll(p, "ssdp:alive")
	go m.serve(ctx, p)
	alive := time.NewTicker(notifyEvery)
	defer alive.Stop()
	for {
		select {
		case <-ctx.Done():
			m.notifyAll(p, "ssdp:byebye")
			return nil
		case <-tick.C:
			if m.rescan(p) {
				m.notifyAll(p, "ssdp:alive")
			}
		case <-alive.C:
			m.notifyAll(p, "ssdp:alive")
		}
	}
}

// listen — сокет группы: ListenMulticastUDP делит порт 1900 со службой Windows «Обнаружение SSDP» (SO_REUSEADDR),
// а приём своих пакетов, который он выключает, включается обратно — иначе поиск с этого же ПК не находит
// сервер (исследование, раздел 5).
func (m *Module) listen(first Iface) (*ipv4.PacketConn, error) {
	ifi, err := net.InterfaceByIndex(first.Index)
	if err != nil {
		return nil, err
	}
	c, err := net.ListenMulticastUDP("udp4", ifi, net.UDPAddrFromAddrPort(m.o.Group))
	if err != nil {
		return nil, err
	}
	p := ipv4.NewPacketConn(c)
	if err := p.SetMulticastLoopback(true); err != nil {
		m.o.Log.Info("обнаружение: приём своих пакетов не включился", "err", err)
	}
	m.mu.Lock()
	m.joined[first.Index] = first
	m.mu.Unlock()
	return p, nil
}

// rescan — подключиться к группе на новых интерфейсах домашней сети, отключиться от пропавших. true —
// что-то изменилось.
func (m *Module) rescan(p *ipv4.PacketConn) bool {
	now := map[int]Iface{}
	for _, i := range m.o.Ifaces() {
		now[i.Index] = i
	}
	group := &net.UDPAddr{IP: net.IP(m.o.Group.Addr().AsSlice())}
	m.mu.Lock()
	defer m.mu.Unlock()
	changed := false
	for idx, i := range now {
		if old, ok := m.joined[idx]; ok {
			if old.Prefix != i.Prefix {
				m.joined[idx], changed = i, true
			}
			continue
		}
		ifi, err := net.InterfaceByIndex(idx)
		if err != nil {
			continue
		}
		if err := p.JoinGroup(ifi, group); err != nil {
			m.o.Log.Info("обнаружение: интерфейс не подключился к группе", "iface", i.Name, "err", err)
			continue
		}
		m.joined[idx], changed = i, true
	}
	for idx, i := range m.joined {
		if _, ok := now[idx]; ok {
			continue
		}
		if ifi, err := net.InterfaceByIndex(idx); err == nil {
			p.LeaveGroup(ifi, group)
		}
		delete(m.joined, idx)
		changed = true
		m.o.Log.Info("обнаружение: интерфейс пропал", "iface", i.Name)
	}
	return changed
}

func (m *Module) ifaces() []Iface {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Iface, 0, len(m.joined))
	for _, i := range m.joined {
		out = append(out, i)
	}
	return out
}

// serve — ответы на поиск, пока сокет открыт.
func (m *Module) serve(ctx context.Context, p *ipv4.PacketConn) {
	buf := make([]byte, 4096)
	for ctx.Err() == nil {
		n, _, from, err := p.ReadFrom(buf)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			continue
		}
		st, ok := ParseSearch(buf[:n])
		if !ok || !Matches(st, m.o.UUID) {
			continue
		}
		ua, ok := from.(*net.UDPAddr)
		if !ok {
			continue
		}
		addr, ok := netip.AddrFromSlice(ua.IP)
		if !ok {
			continue
		}
		loc, ok := LocationFor(addr, m.ifaces(), m.o.APIPort)
		if !ok {
			continue
		}
		if _, err := p.WriteTo(Reply(st, loc, m.o.UUID, m.o.Server), nil, ua); err != nil {
			m.o.Log.Info("обнаружение: ответ не ушёл", "to", ua.String(), "err", err)
		}
	}
}

// notifyAll — объявление в группу на каждом интерфейсе домашней сети (LOCATION — адрес этого интерфейса).
func (m *Module) notifyAll(p *ipv4.PacketConn, nts string) {
	dst := net.UDPAddrFromAddrPort(m.o.Group)
	for _, i := range m.ifaces() {
		ifi, err := net.InterfaceByIndex(i.Index)
		if err != nil {
			continue
		}
		if err := p.SetMulticastInterface(ifi); err != nil {
			continue
		}
		if _, err := p.WriteTo(notify(nts, locationOn(i, m.o.APIPort), m.o.UUID, m.o.Server, m.o.Group), nil, dst); err != nil {
			m.o.Log.Info("обнаружение: объявление не ушло", "iface", i.Name, "err", err)
		}
	}
}
