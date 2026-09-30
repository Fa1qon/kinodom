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
		"t1.info":                        "https://t1.info",
		"https://t1.info/":               "https://t1.info",
		"  http://www.t1.info/top/0  ":   "http://t1.info",
		"HTTPS://WWW.t1.Is":              "https://t1.is",
		"t2.org/forum/index.php?x=1": "https://t2.org",
		"https://mirror.example:8443/a#b":   "https://mirror.example:8443",
		"http://127.0.0.1:18090":            "http://127.0.0.1:18090",
	}
	for in, want := range ok {
		got, err := SiteAddress(in)
		if err != nil || got != want {
			t.Errorf("SiteAddress(%q) = %q, %v; ждали %q", in, got, err, want)
		}
	}
	bad := []string{"", "   ", "абв", "ftp://t1.info", "https://", "http:///path", "t1", "rut or.info", "https://user:pw@t1.info"}
	for _, in := range bad {
		if got, err := SiteAddress(in); err == nil {
			t.Errorf("SiteAddress(%q) = %q без ошибки", in, got)
		}
	}
}

// Служебные адреса выводятся из адреса сайта (спека этапа 11a, раздел 6): схема — как у сайта,
// у адреса-IP (тестовый сервер, свой прокси-сайт) служебные — он сам.
func TestServiceAddresses(t *testing.T) {
	if got := t1Download("https://t1.info"); got != "https://d.t1.info" {
		t.Errorf("t1Download = %q", got)
	}
	if got := t1Download("http://new-t1.org:8080"); got != "http://d.new-t1.org:8080" {
		t.Errorf("t1Download с портом = %q", got)
	}
	api, feed := t2Service("https://t2.org")
	if api != "https://api.t2.cc" || feed != "https://feed.t2.cc" {
		t.Errorf("t2Service = %q, %q", api, feed)
	}
	api, feed = t2Service("http://t2.net")
	if api != "http://api.t2.cc" || feed != "http://feed.t2.cc" {
		t.Errorf("t2Service http = %q, %q", api, feed)
	}
	if got := t1Download("http://127.0.0.1:5000"); got != "http://127.0.0.1:5000" {
		t.Errorf("t1Download для IP = %q", got)
	}
	if api, feed = t2Service("http://127.0.0.1:5000"); api != "http://127.0.0.1:5000" || feed != api {
		t.Errorf("t2Service для IP = %q, %q", api, feed)
	}
	if got := t1Download(""); got != "" {
		t.Errorf("t1Download пустого = %q", got)
	}
}

func TestErrNotConfiguredIsNetx(t *testing.T) {
	if !errors.Is(ErrNotConfigured, netx.ErrNotConfigured) {
		t.Fatal("source.ErrNotConfigured — не та же ошибка, что у netx")
	}
}
