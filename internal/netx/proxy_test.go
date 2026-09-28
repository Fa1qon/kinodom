package netx

import (
	"strings"
	"testing"
)

func TestParseProxyAccepts(t *testing.T) {
	cases := map[string]string{
		"":                                "",
		"socks5://127.0.0.1:1080":         "socks5://127.0.0.1:1080",
		" http://127.0.0.1:8080 ":         "http://127.0.0.1:8080",
		"http://user:pass@proxy.lan:3128": "http://user:pass@proxy.lan:3128",
	}
	for in, want := range cases {
		u, err := ParseProxy(in)
		if err != nil {
			t.Errorf("ParseProxy(%q): %v", in, err)
			continue
		}
		got := ""
		if u != nil {
			got = u.String()
		}
		if got != want {
			t.Errorf("ParseProxy(%q) = %q, ожидалось %q", in, got, want)
		}
	}
}

func TestParseProxyRejectsWithHint(t *testing.T) {
	for _, in := range []string{"127.0.0.1:1080", "localhost:1080", "ftp://x:21", "socks5://127.0.0.1", "socks5://"} {
		_, err := ParseProxy(in)
		if err == nil || !strings.Contains(err.Error(), "прокси") {
			t.Errorf("ParseProxy(%q): ожидалась понятная ошибка, получено %v", in, err)
		}
	}
}
