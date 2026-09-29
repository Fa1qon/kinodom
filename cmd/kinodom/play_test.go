package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"kinodom/internal/httpx"
	"kinodom/internal/torrents"
)

func TestPlayFlowAgainstFakeServer(t *testing.T) {
	hash := strings.Repeat("ab", 20)
	statusCalls, fileCalls := 0, 0
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/torrents", func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)
		if req["magnet"] != "magnet:?xt=urn:btih:"+hash {
			httpx.WriteError(w, 400, "не тот magnet")
			return
		}
		httpx.WriteJSON(w, 200, map[string]string{"hash": hash})
	})
	mux.HandleFunc("GET /api/v1/torrents/{hash}", func(w http.ResponseWriter, r *http.Request) {
		statusCalls++
		st := torrents.TorrentStatus{Hash: hash, State: torrents.StateConnecting, Files: []torrents.FileProgress{}}
		if statusCalls > 1 {
			st.State, st.Peers = torrents.StateReady, 3
			st.Files = []torrents.FileProgress{{FileInfo: torrents.FileInfo{Index: 2, Name: "Серия 1.mkv", Size: 1}},
				{FileInfo: torrents.FileInfo{Index: 0, Name: "Серия 2.mkv", Size: 1}}}
		}
		httpx.WriteJSON(w, 200, st)
	})
	mux.HandleFunc("POST /api/v1/torrents/{hash}/files/{index}/prepare", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("index") != "0" || r.Header.Get("Content-Type") != "application/json" {
			httpx.WriteError(w, 400, "не тот файл или не JSON")
			return
		}
		httpx.WriteJSON(w, 202, struct{}{})
	})
	mux.HandleFunc("GET /api/v1/torrents/{hash}/files/{index}", func(w http.ResponseWriter, r *http.Request) {
		fileCalls++
		resp := map[string]any{"state": "buffering", "bufferPercent": 40, "peers": 3, "speed": 3 << 20, "smoothInSec": 600, "streamUrl": ""}
		if fileCalls > 1 {
			resp["state"], resp["bufferPercent"] = "ready", 100
			resp["streamUrl"] = "http://127.0.0.1:8090/stream/" + hash + "/0/a.mkv"
		}
		httpx.WriteJSON(w, 200, resp)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	var out strings.Builder
	p := &playFlow{base: srv.URL, out: &out, client: srv.Client(), poll: time.Millisecond, limit: 5 * time.Second}
	u, err := p.play(context.Background(), "magnet:?xt=urn:btih:"+hash, 0)
	if err != nil {
		t.Fatal(err)
	}
	if u != "http://127.0.0.1:8090/stream/"+hash+"/0/a.mkv" {
		t.Fatalf("ссылка %q", u)
	}
	for _, want := range []string{"ищем участников раздачи", "буферизация 40 %", "без остановок через ~10 мин"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("в выводе нет %q:\n%s", want, out.String())
		}
	}
}

func TestPlayShowsAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteError(w, 400, "magnet-ссылка не читается")
	}))
	defer srv.Close()
	p := &playFlow{base: srv.URL, out: &strings.Builder{}, client: srv.Client(), poll: time.Millisecond, limit: time.Second}
	if _, err := p.play(context.Background(), "magnet:?xt=bad", -1); err == nil || err.Error() != "magnet-ссылка не читается" {
		t.Fatalf("ожидался текст ошибки сервера, получено %v", err)
	}
}

func TestPlayServerDown(t *testing.T) {
	p := &playFlow{base: "http://127.0.0.1:1", out: &strings.Builder{}, client: &http.Client{Timeout: time.Second}, poll: time.Millisecond, limit: time.Second}
	if _, err := p.play(context.Background(), "magnet:?xt=urn:btih:"+strings.Repeat("ab", 20), -1); err == nil || !strings.Contains(err.Error(), "не отвечает") {
		t.Fatalf("ожидалось «сервер не отвечает», получено %v", err)
	}
}

func TestLanURLsKeepPathAndPort(t *testing.T) {
	for _, l := range lanURLs("http://127.0.0.1:8090/stream/ab/0/%D0%A4.mkv") {
		u, err := url.Parse(l.url)
		if err != nil {
			t.Fatal(err)
		}
		ip := net.ParseIP(u.Hostname())
		if u.Port() != "8090" || u.EscapedPath() != "/stream/ab/0/%D0%A4.mkv" || ip.To4() == nil || !ip.IsPrivate() {
			t.Errorf("%s (%s): неверная ссылка для телевизора", l.url, l.iface)
		}
	}
}
