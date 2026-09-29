package m3u

import (
	"reflect"
	"testing"

	"golang.org/x/text/encoding/charmap"
)

// Строки — из настоящих плейлистов (iptv-org, «мега», плейлист заказчика без ссылок); ссылки
// плейлиста заказчика заменены.
const sample = "\uFEFF#EXTM3U url-tvg=\"https://iptvx.one/EPG\"\n" +
	`#EXTINF:-1 tvg-id="2x2.ru@SD" tvg-logo="https://i.imgur.com/fhQFLEl.png" group-title="Entertainment",2x2 (576i)
https://bl.rutube.ru/livestream/392b/index.m3u8?e=2068731801&scheme=https
#EXTINF:-1 group-title="Общие" tvg-id="mir-pl7" catchup="append" catchup-source="?offset=-${offset}&utcstart=${timestamp}", Мир +7 (Архив)
#EXTVLCOPT:http-user-agent=WINK/1.40.1 (AndroidTV/9) HlsWinkPlayer
https://zabava.example/hls/CH_MIR/variant.m3u8
#EXTINF:-1 tvg-id="BlackSeaTV.ua@SD" http-user-agent="curl/8.16.0" group-title="General",Black Sea TV
#EXTVLCOPT:http-user-agent=curl/8.16.0
https://ext.cdn.nashnet.tv/228.0.0.136/index.m3u8
#EXTINF:-1 tvg-id="Aastha.in@SD" http-user-agent="#EXTVLCOPT:http-user-agent=Mozilla/5.0 (Windows NT 10.0)" group-title="Religious",Aastha (576p)
https://aastha.example/live.m3u8
#EXTINF:-1 tvg-name="Кино, ТВ" tvg-shift="+4",Кино, ТВ HD
#EXTGRP:Кино
#EXTHTTP:{"User-Agent":"Lavf/60","Referer":"https://site.example/"}
http://kino.example/stream.ts
#EXTINF:-1 group-title="Мульт",Мультик
http://cartoon.example/live|User-Agent=Kodi%2F20&Referer=https://r.example/
#EXTINF:-1,Мультикаст
udp://@239.1.1.1:1234
#EXTINF:-1,RTMP
rtmp://live.example/app/stream
#EXTINF:-1,Без ссылки
#EXTINF:-1 group-title="4K VIDEO (VPN)", 4K UHD релакс
https://river.example/a.mp4.m3u8?i=3840x2160
`

func TestParse(t *testing.T) {
	p := Parse([]byte(sample))
	want := []Entry{
		{Name: "2x2 (576i)", TvgID: "2x2.ru@SD", Logo: "https://i.imgur.com/fhQFLEl.png", Group: "Entertainment",
			URL: "https://bl.rutube.ru/livestream/392b/index.m3u8?e=2068731801&scheme=https"},
		{Name: "Мир +7 (Архив)", TvgID: "mir-pl7", Group: "Общие",
			Headers: Headers{UserAgent: "WINK/1.40.1 (AndroidTV/9) HlsWinkPlayer"}, URL: "https://zabava.example/hls/CH_MIR/variant.m3u8"},
		{Name: "Black Sea TV", TvgID: "BlackSeaTV.ua@SD", Group: "General", Headers: Headers{UserAgent: "curl/8.16.0"},
			URL: "https://ext.cdn.nashnet.tv/228.0.0.136/index.m3u8"},
		{Name: "Aastha (576p)", TvgID: "Aastha.in@SD", Group: "Religious", Headers: Headers{UserAgent: "Mozilla/5.0 (Windows NT 10.0)"},
			URL: "https://aastha.example/live.m3u8"},
		{Name: "Кино, ТВ HD", TvgName: "Кино, ТВ", Shift: 4, Group: "Кино",
			Headers: Headers{UserAgent: "Lavf/60", Referrer: "https://site.example/"}, URL: "http://kino.example/stream.ts"},
		{Name: "Мультик", Group: "Мульт", Headers: Headers{UserAgent: "Kodi/20", Referrer: "https://r.example/"},
			URL: "http://cartoon.example/live"},
		{Name: "4K UHD релакс", Group: "4K VIDEO (VPN)", URL: "https://river.example/a.mp4.m3u8?i=3840x2160"},
	}
	if !reflect.DeepEqual(p.Entries, want) {
		for i := range max(len(p.Entries), len(want)) {
			var g, w Entry
			if i < len(p.Entries) {
				g = p.Entries[i]
			}
			if i < len(want) {
				w = want[i]
			}
			if !reflect.DeepEqual(g, w) {
				t.Errorf("запись %d:\n получили %+v\n нужно    %+v", i, g, w)
			}
		}
	}
	if p.Unsupported != 2 {
		t.Errorf("не поддерживается: %d, нужно 2 (udp и rtmp)", p.Unsupported)
	}
}

// Плейлисты в windows-1251 бывают: текст не UTF-8 — читается как windows-1251.
func TestParseWindows1251(t *testing.T) {
	b, err := charmap.Windows1251.NewEncoder().Bytes([]byte("#EXTM3U\n#EXTINF:-1 group-title=\"Кино\",Первый канал\nhttp://a.example/1.m3u8\n"))
	if err != nil {
		t.Fatal(err)
	}
	p := Parse(b)
	if len(p.Entries) != 1 || p.Entries[0].Name != "Первый канал" || p.Entries[0].Group != "Кино" {
		t.Fatalf("записи %+v", p.Entries)
	}
}

// Перевод строки \r\n и пустые строки не мешают; мусор вместо плейлиста — ни одной записи.
func TestParseCRLFAndGarbage(t *testing.T) {
	p := Parse([]byte("#EXTM3U\r\n\r\n#EXTINF:-1,Канал\r\nhttp://a.example/x.m3u8\r\n"))
	if len(p.Entries) != 1 || p.Entries[0].URL != "http://a.example/x.m3u8" || p.Entries[0].Name != "Канал" {
		t.Fatalf("записи %+v", p.Entries)
	}
	bom := string([]byte{0xEF, 0xBB, 0xBF})
	if p := Parse([]byte(bom + "#EXTINF:-1,Канал\nhttp://a.example/x.m3u8\n")); len(p.Entries) != 1 || p.Entries[0].Name != "Канал" {
		t.Fatalf("плейлист с BOM без заголовка: %+v", p.Entries)
	}
	if p := Parse([]byte("<html><body>404</body></html>")); len(p.Entries) != 0 {
		t.Fatalf("из HTML разобрались записи: %+v", p.Entries)
	}
}

func TestKindOf(t *testing.T) {
	cases := map[string]string{
		"http://a/b/index.m3u8":        KindHLS,
		"http://a/b.m3u8?token=1":      KindHLS,
		"http://a/play?format=m3u8":    KindHLS,
		"https://a/manifest.mpd":       KindDASH,
		"http://a/stream.ts":           "",
		"http://a:8080/udp/239.1.1.1":  "",
		"http://a/live/playlist.M3U8":  KindHLS,
		"http://a/x.mpd?auth=abc&y=1":  KindDASH,
		"http://a/m3u8-not-really/abc": "",
	}
	for u, want := range cases {
		if got := KindOf(u); got != want {
			t.Errorf("%s: вид %q, нужно %q", u, got, want)
		}
	}
}

func TestQualityOf(t *testing.T) {
	cases := map[string]string{
		"Первый канал HD":           "HD",
		"Матч! Премьер FHD":         "FHD",
		"Кино UHD":                  "4K",
		"4K UHD релакс":             "4K",
		"2x2 (576i)":                "SD",
		"1HD Music (1080p)":         "FHD",
		"Arsenal (720p)":            "HD",
		"Discovery 2160p":           "4K",
		"Россия 1 SD":               "SD",
		"Пятница!":                  "",
		"1HD Music Television":      "",
		"Нано ТВ (480p) [Not 24/7]": "SD",
	}
	for name, want := range cases {
		if got := QualityOf(name); got != want {
			t.Errorf("%q: качество %q, нужно %q", name, got, want)
		}
	}
}
