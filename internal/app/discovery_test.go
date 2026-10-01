package app

import (
	"context"
	"encoding/xml"
	"net/http"
	"testing"
)

// Сервер объявляет себя по SSDP (спека этапа 13, раздел 5.1): описание /upnp/desc.xml с адресом пульта и
// постоянным UUID — между запусками он тот же; модуль виден в «Состоянии».
func TestDiscoveryDescAndUUID(t *testing.T) {
	home := t.TempDir()
	opts := Options{Home: home, ListenAddr: "127.0.0.1:0", Offline: true, DownloadsDir: t.TempDir()}
	udn := func(a *App) string {
		t.Helper()
		resp, err := http.Get("http://" + a.API.Addr() + "/upnp/desc.xml")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var d struct {
			Device struct {
				UDN             string `xml:"UDN"`
				PresentationURL string `xml:"presentationURL"`
			} `xml:"device"`
		}
		if resp.StatusCode != 200 || xml.NewDecoder(resp.Body).Decode(&d) != nil {
			t.Fatalf("описание: %d", resp.StatusCode)
		}
		if d.Device.PresentationURL != "http://"+a.API.Addr()+"/" {
			t.Fatalf("presentationURL %q", d.Device.PresentationURL)
		}
		return d.Device.UDN
	}
	a1 := startAppWith(t, opts)
	first := udn(a1)
	if len(first) < len("uuid:")+36 {
		t.Fatalf("UDN %q", first)
	}
	var st struct {
		Modules []struct{ Name, State string }
	}
	getJSON(t, "http://"+a1.API.Addr()+"/api/v1/status", &st)
	found := false
	for _, m := range st.Modules {
		found = found || m.Name == "discovery"
	}
	if !found {
		t.Fatalf("модуля discovery нет в «Состоянии»: %+v", st.Modules)
	}
	stored, ok, err := a1.DB.Setting(context.Background(), "discovery.uuid")
	if err != nil || !ok || "uuid:"+stored != first {
		t.Fatalf("UUID в настройках %q (%v, %v), в описании %q", stored, ok, err, first)
	}
}
