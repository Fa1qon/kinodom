package source

import (
	"errors"
	"testing"

	"kinodom/internal/netx"
)

// Адрес трекера вводит человек — как придётся: без схемы, с www., с путём, с пробелами. Итог один:
// «схема://хост» (Review Focus 2 плана этапа 11a).
func TestTrackerAddressNormalize(t *testing.T) {
	ok := map[string]string{
		"rutor.info":                        "https://rutor.info",
		"https://rutor.info/":               "https://rutor.info",
		"  http://www.rutor.info/top/0  ":   "http://rutor.info",
		"HTTPS://WWW.Rutor.Is":              "https://rutor.is",
		"rutracker.org/forum/index.php?x=1": "https://rutracker.org",
		"https://mirror.example:8443/a#b":   "https://mirror.example:8443",
		"http://127.0.0.1:18090":            "http://127.0.0.1:18090",
	}
	for in, want := range ok {
		got, err := SiteAddress(in)
		if err != nil || got != want {
			t.Errorf("SiteAddress(%q) = %q, %v; ждали %q", in, got, err, want)
		}
	}
	bad := []string{"", "   ", "абв", "ftp://rutor.info", "https://", "http:///path", "rutor", "rut or.info", "https://user:pw@rutor.info"}
	for _, in := range bad {
		if got, err := SiteAddress(in); err == nil {
			t.Errorf("SiteAddress(%q) = %q без ошибки", in, got)
		}
	}
}

// Служебные адреса выводятся из адреса сайта (спека этапа 11a, раздел 6): схема — как у сайта,
// у адреса-IP (тестовый сервер, свой прокси-сайт) служебные — он сам.
func TestServiceAddresses(t *testing.T) {
	if got := RutorDownload("https://rutor.info"); got != "https://d.rutor.info" {
		t.Errorf("RutorDownload = %q", got)
	}
	if got := RutorDownload("http://new-rutor.org:8080"); got != "http://d.new-rutor.org:8080" {
		t.Errorf("RutorDownload с портом = %q", got)
	}
	api, feed := RutrackerService("https://rutracker.org")
	if api != "https://api.rutracker.cc" || feed != "https://feed.rutracker.cc" {
		t.Errorf("RutrackerService = %q, %q", api, feed)
	}
	api, feed = RutrackerService("http://rutracker.net")
	if api != "http://api.rutracker.cc" || feed != "http://feed.rutracker.cc" {
		t.Errorf("RutrackerService http = %q, %q", api, feed)
	}
	if got := RutorDownload("http://127.0.0.1:5000"); got != "http://127.0.0.1:5000" {
		t.Errorf("RutorDownload для IP = %q", got)
	}
	if api, feed = RutrackerService("http://127.0.0.1:5000"); api != "http://127.0.0.1:5000" || feed != api {
		t.Errorf("RutrackerService для IP = %q, %q", api, feed)
	}
	if got := RutorDownload(""); got != "" {
		t.Errorf("RutorDownload пустого = %q", got)
	}
}

func TestErrNotConfiguredIsNetx(t *testing.T) {
	if !errors.Is(ErrNotConfigured, netx.ErrNotConfigured) {
		t.Fatal("source.ErrNotConfigured — не та же ошибка, что у netx")
	}
}
