package probe

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"syscall"
	"testing"
	"time"

	"kinodom/internal/iptv/m3u"
)

// chunks — отдать body частями с паузой между ними (медленный источник).
func chunks(w http.ResponseWriter, body []byte, n int, pause time.Duration) {
	w.Header().Set("Content-Length", fmt.Sprint(len(body)))
	step := len(body) / n
	for i := 0; i < n; i++ {
		part := body[i*step : (i+1)*step]
		if i == n-1 {
			part = body[i*step:]
		}
		w.Write(part)
		w.(http.Flusher).Flush()
		if i < n-1 {
			time.Sleep(pause)
		}
	}
}

// hlsServer — фейковый источник HLS: мастер-плейлист (1080p и 720p), плейлист варианта с тремя
// сегментами по 0,5 с; сегмент — 32 КБ, отдаётся за pauses пауз по pause.
func hlsServer(t *testing.T, pauses int, pause time.Duration) *httptest.Server {
	t.Helper()
	seg := make([]byte, 32<<10)
	mux := http.NewServeMux()
	mux.HandleFunc("/live/master.m3u8", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=300000,RESOLUTION=1280x720\nlow/index.m3u8\n"+
			"#EXT-X-STREAM-INF:BANDWIDTH=524288,RESOLUTION=1920x1080\nhigh/index.m3u8\n")
	})
	mux.HandleFunc("/live/high/index.m3u8", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXTINF:0.5,\nseg1.ts\n#EXTINF:0.5,\nseg2.ts\n#EXTINF:0.5,\nseg3.ts\n")
	})
	mux.HandleFunc("/live/high/seg3.ts", func(w http.ResponseWriter, r *http.Request) { chunks(w, seg, pauses+1, pause) })
	mux.HandleFunc("/plain/index.m3u8", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "#EXTM3U\n#EXTINF:0.5,\n/live/high/seg3.ts\n")
	})
	mux.HandleFunc("/empty.m3u8", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "#EXTM3U\n#EXT-X-ENDLIST\n") })
	mux.HandleFunc("/broken.m3u8", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "#EXTM3U\n#EXTINF:0.5,\nnone.ts\n") })
	mux.HandleFunc("/forbidden.m3u8", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusForbidden) })
	mux.HandleFunc("/page", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<html><body>Сайт</body></html>")
	})
	mux.HandleFunc("/manifest.mpd", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<?xml version="1.0"?><MPD xmlns="urn:mpeg:dash:schema:mpd:2011"></MPD>`)
	})
	mux.HandleFunc("/ua.m3u8", func(w http.ResponseWriter, r *http.Request) {
		if r.UserAgent() != "WINK/1.40" || r.Referer() != "https://wink.example/" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		fmt.Fprint(w, "#EXTM3U\n#EXTINF:0.5,\n/live/high/seg3.ts\n")
	})
	mux.HandleFunc("/vlc.m3u8", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.UserAgent(), "VLC/") {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		fmt.Fprint(w, "#EXTM3U\n#EXTINF:0.5,\n/live/high/seg3.ts\n")
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func prober() *Prober {
	return &Prober{Client: &http.Client{}, Timeout: 2 * time.Second, SegmentCap: 512 << 10, LiveFor: time.Second, Pause: 200 * time.Millisecond}
}

// Полная проверка HLS: вариант с наибольшим BANDWIDTH, последний сегмент; запас = скорость ÷ битрейт
// (спека этапа 8, раздел 5.8): 🟢 ≥ 1,5×, 🟡 1–1,5×, 🔴 < 1×. Качество — по RESOLUTION.
func TestFullHLS(t *testing.T) {
	cases := []struct {
		name   string
		pauses int
		pause  time.Duration
		want   string
	}{
		{"быстрый", 0, 0, GradeGreen},
		{"впритык", 3, 150 * time.Millisecond, GradeYellow}, // 32 КБ за ~0,45 с при битрейте 64 КБ/с — ~1,1×
		{"медленный", 3, 300 * time.Millisecond, GradeRed},  // ~0,9 с — ~0,55×
	}
	for _, c := range cases {
		srv := hlsServer(t, c.pauses, c.pause)
		r := prober().Full(context.Background(), Target{URL: srv.URL + "/live/master.m3u8"})
		if r.Grade != c.want || r.Kind != m3u.KindHLS || r.Height != 1080 {
			t.Errorf("%s: %+v, нужно %s, hls, 1080", c.name, r, c.want)
		}
		if r.Grade != GradeGreen && (r.Ratio <= 0 || r.Ratio > 2) {
			t.Errorf("%s: запас %v", c.name, r.Ratio)
		}
	}
}

func TestFullHLSWithoutMaster(t *testing.T) {
	srv := hlsServer(t, 0, 0)
	r := prober().Full(context.Background(), Target{URL: srv.URL + "/plain/index.m3u8"})
	if r.Grade != GradeGreen || r.Mbps <= 0 || r.Height != 0 {
		t.Errorf("без мастер-плейлиста: %+v", r)
	}
}

func TestProbeErrors(t *testing.T) {
	srv := hlsServer(t, 0, 0)
	silent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer silent.Close()
	p := prober()
	p.Timeout = 300 * time.Millisecond
	cases := []struct {
		url   string
		full  bool
		grade string
		err   string
	}{
		{srv.URL + "/forbidden.m3u8", false, GradeBlack, "HTTP 403"},
		{srv.URL + "/forbidden.m3u8", true, GradeBlack, "HTTP 403"},
		{silent.URL + "/x.m3u8", false, GradeBlack, "нет ответа за 0,3 с"},
		{srv.URL + "/page", false, GradeBlack, "ответ — не видео"},
		{srv.URL + "/empty.m3u8", false, GradeBlack, "плейлист пуст"},
		{srv.URL + "/broken.m3u8", false, GradeAlive, ""}, // лёгкая сегменты не качает
		{srv.URL + "/broken.m3u8", true, GradeRed, "сегмент не скачался"},
	}
	for _, c := range cases {
		var r Result
		if c.full {
			r = p.Full(context.Background(), Target{URL: c.url})
		} else {
			r = p.Light(context.Background(), Target{URL: c.url})
		}
		if r.Grade != c.grade || r.Error != c.err {
			t.Errorf("%s (полная %v): %q %q, нужно %q %q", c.url, c.full, r.Grade, r.Error, c.grade, c.err)
		}
	}
}

// Ошибки — текстом для человека (сеть тесту не нужна: DNS на этом ПК отвечает об отсутствии адреса
// дольше 10 с).
func TestDescribe(t *testing.T) {
	p := prober()
	cases := map[error]string{
		&net.DNSError{Err: "no such host", Name: "x.invalid", IsNotFound: true}: "адрес не найден (DNS)",
		context.DeadlineExceeded:                            "нет ответа за 2 с",
		&net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}: "соединение отклонено",
		errText("HTTP 404"):                                 "HTTP 404",
		errors.New("tls: handshake failure"):                "ошибка защищённого соединения",
		io.ErrUnexpectedEOF:                                 "обрыв соединения",
	}
	for err, want := range cases {
		if got := p.describe(err); got != want {
			t.Errorf("%v: %q, нужно %q", err, got, want)
		}
	}
}

// Заголовки из плейлиста идут в проверку; без них — User-Agent VLC, как у плеера.
func TestProbeHeaders(t *testing.T) {
	srv := hlsServer(t, 0, 0)
	p := prober()
	if r := p.Light(context.Background(), Target{URL: srv.URL + "/ua.m3u8", Headers: m3u.Headers{UserAgent: "WINK/1.40", Referrer: "https://wink.example/"}}); r.Grade != GradeAlive {
		t.Errorf("с заголовками: %+v", r)
	}
	if r := p.Light(context.Background(), Target{URL: srv.URL + "/vlc.m3u8"}); r.Grade != GradeAlive {
		t.Errorf("User-Agent VLC: %+v", r)
	}
}

func TestDASH(t *testing.T) {
	srv := hlsServer(t, 0, 0)
	for _, full := range []bool{false, true} {
		var r Result
		if full {
			r = prober().Full(context.Background(), Target{URL: srv.URL + "/manifest.mpd"})
		} else {
			r = prober().Light(context.Background(), Target{URL: srv.URL + "/manifest.mpd"})
		}
		if r.Grade != GradeAlive || r.Kind != m3u.KindDASH {
			t.Errorf("DASH (полная %v): %+v", full, r)
		}
	}
}

// Живой поток (не HLS): лёгкая — первые 64 КБ; полная — приём LiveFor, паузы дольше Pause: 🟢 без
// пауз, 🟡 одна, 🔴 больше.
func TestLiveStream(t *testing.T) {
	stream := func(pauseEvery int, pause time.Duration) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "video/mp2t")
			buf := make([]byte, 16<<10)
			for i := 1; ; i++ {
				if _, err := w.Write(buf); err != nil {
					return
				}
				w.(http.Flusher).Flush()
				d := 50 * time.Millisecond
				if pauseEvery > 0 && i%pauseEvery == 0 {
					d = pause
				}
				select {
				case <-r.Context().Done():
					return
				case <-time.After(d):
				}
			}
		}))
	}
	cases := []struct {
		every int
		want  string
	}{{0, GradeGreen}, {12, GradeYellow}, {3, GradeRed}}
	for _, c := range cases {
		srv := stream(c.every, 350*time.Millisecond)
		p := prober()
		if r := p.Light(context.Background(), Target{URL: srv.URL + "/live"}); r.Grade != GradeAlive || r.Kind != m3u.KindLive {
			t.Errorf("лёгкая: %+v", r)
		}
		r := p.Full(context.Background(), Target{URL: srv.URL + "/live"})
		if r.Grade != c.want || r.Kind != m3u.KindLive || r.Mbps <= 0 {
			t.Errorf("пауза каждые %d: %+v, нужно %s", c.every, r, c.want)
		}
		srv.Close()
	}
}

// Заглушка провайдера вместо эфира (замечание № 15): Ростелеком вне своей зоны перенаправляет плейлист
// потока на запись «не показывает видео на этой территории» — 4 сегмента и конец записи. Запись — не эфир:
// и лёгкая, и полная проверка — мёртв с причиной; «событие» без конца записи и обычный эфир — живы.
func TestProbeStubPlaylist(t *testing.T) {
	seg := make([]byte, 32<<10)
	mux := http.NewServeMux()
	mux.HandleFunc("/hls/CH_1TVSD_4/variant.m3u8", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "#EXTM3U\n#EXT-X-VERSION:5\n#EXT-X-STREAM-INF:PROGRAM-ID=1,BANDWIDTH=2698704,CODECS=\"avc1.64001e,mp4a.40.29\",RESOLUTION=800x450\n/hls/CH_1TVSD_4/playlist.m3u8\n")
	})
	mux.HandleFunc("/hls/CH_1TVSD_4/playlist.m3u8", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/rtk_block.m3u8", http.StatusMovedPermanently)
	})
	mux.HandleFunc("/rtk_block.m3u8", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:10\n#EXT-X-MEDIA-SEQUENCE:0\n"+
			"#EXTINF:10.000000,\n/index0.ts\n#EXTINF:10.000000,\n/index1.ts\n#EXTINF:10.000000,\n/index2.ts\n#EXTINF:10.000000,\n/index3.ts\n#EXT-X-ENDLIST\n")
	})
	mux.HandleFunc("/vod.m3u8", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "#EXTM3U\n#EXT-X-PLAYLIST-TYPE:VOD\n#EXT-X-TARGETDURATION:10\n#EXTINF:10,\n/index3.ts\n")
	})
	mux.HandleFunc("/event.m3u8", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "#EXTM3U\n#EXT-X-PLAYLIST-TYPE:EVENT\n#EXT-X-TARGETDURATION:1\n#EXTINF:0.5,\n/index3.ts\n#EXTINF:0.5,\n/index3.ts\n")
	})
	mux.HandleFunc("/live.m3u8", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXT-X-MEDIA-SEQUENCE:1790830800\n#EXTINF:0.5,\n/index3.ts\n")
	})
	mux.HandleFunc("/index3.ts", func(w http.ResponseWriter, r *http.Request) { w.Write(seg) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	p := prober()
	for _, c := range []struct {
		path string
		stub bool
	}{{"/hls/CH_1TVSD_4/variant.m3u8", true}, {"/vod.m3u8", true}, {"/event.m3u8", false}, {"/live.m3u8", false}} {
		for name, check := range map[string]func(context.Context, Target) Result{"лёгкая": p.Light, "полная": p.Full} {
			r := check(context.Background(), Target{URL: srv.URL + c.path})
			if stub := r.Grade == GradeBlack && r.Error == StubError; stub != c.stub || (!c.stub && r.Grade == GradeBlack) {
				t.Errorf("%s %s: %s %q", name, c.path, r.Grade, r.Error)
			}
		}
	}
}
