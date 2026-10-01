package app

import (
	"context"
	"crypto/rand"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"

	"kinodom/internal/appdist"
	"kinodom/internal/discovery"
)

// initDiscovery — сервер объявляет себя в домашней сети по SSDP (спека этапа 13, раздел 5.1): приложение для ТВ
// и телефона находит его само. UUID — постоянный (настройка discovery.uuid, создаётся при первом запуске).
func (a *App) initDiscovery(ctx context.Context, o Options) {
	uuid, ok, err := a.DB.Setting(ctx, "discovery.uuid")
	if err != nil || !ok || uuid == "" {
		uuid = newUUID()
		if err := a.DB.SetSetting(ctx, "discovery.uuid", uuid); err != nil {
			a.Log.Warn("обнаружение: UUID не записался — после перезапуска будет другой", "err", err)
		}
	}
	name, _ := os.Hostname()
	_, p, _ := net.SplitHostPort(a.API.Addr())
	port, _ := strconv.Atoi(p)
	d := discovery.New(discovery.Options{APIPort: port, Name: name, UUID: uuid, Server: "Kinodom/" + a.version,
		Group: o.DiscoveryGroup, Log: a.Log.With("module", "discovery")})
	d.Register(a.API)
	a.Sup.Add(d, a.ModuleEnabled(ctx, d.Name()))
	// Приложение для Android — из папки программы (установщик кладёт APK рядом с kinodom.exe; спека этапа 13, 5.3).
	if exe, err := os.Executable(); err == nil {
		appdist.New(filepath.Dir(exe)).Register(a.API)
	}
}

// newUUID — случайный UUID версии 4.
func newUUID() string {
	var b [16]byte
	rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
