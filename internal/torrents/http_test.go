package torrents

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"kinodom/internal/torrents/torrenttest"
)

type testRouter struct{ mux *http.ServeMux }

func (r testRouter) Handle(p, _ string, h http.Handler)      { r.mux.Handle(p, h) }
func (r testRouter) HandleLocal(p, _ string, h http.Handler) { r.mux.Handle(p, h) }

func apiFixture(t *testing.T) (*Service, *httptest.Server) {
	t.Helper()
	s := newTestService(t)
	runService(t, s)
	mux := http.NewServeMux()
	s.Register(testRouter{mux})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return s, srv
}

func call(t *testing.T, method, url string, in, out any) int {
	t.Helper()
	var body *bytes.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	} else {
		body = bytes.NewReader(nil)
	}
	req, _ := http.NewRequest(method, url, body)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode
}

func TestAPIOpenPrepareStream(t *testing.T) {
	s, srv := apiFixture(t)
	src := t.TempDir()
	mi, _ := torrenttest.MakeTorrent(t, src, "film.mkv", 64<<10, torrenttest.File{Path: "film.mkv", Size: 1 << 20})
	seeder, _ := torrenttest.NewSeeder(t, src, mi)

	var opened struct{ Hash string }
	if code := call(t, "POST", srv.URL+"/api/v1/torrents", map[string]any{"torrent": torrentBytes(t, mi)}, &opened); code != 200 {
		t.Fatalf("open: код %d", code)
	}
	connect(t, s, mi.HashInfoBytes(), seeder)

	var st TorrentStatus
	if code := call(t, "GET", srv.URL+"/api/v1/torrents/"+opened.Hash, nil, &st); code != 200 || st.State != StateReady {
		t.Fatalf("status: код %d, %+v", code, st)
	}
	base := fmt.Sprintf("%s/api/v1/torrents/%s/files/%d", srv.URL, opened.Hash, st.Files[0].Index)
	if code := call(t, "POST", base+"/prepare", struct{}{}, nil); code != http.StatusAccepted {
		t.Fatalf("prepare: код %d", code)
	}
	var fs struct {
		State     FileState
		StreamURL string `json:"streamUrl"`
	}
	deadline := time.Now().Add(15 * time.Second)
	for fs.State != FileReady {
		if time.Now().After(deadline) {
			t.Fatal("буфер не готов")
		}
		call(t, "GET", base, nil, &fs)
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.HasPrefix(fs.StreamURL, srv.URL+"/stream/"+opened.Hash+"/") {
		t.Fatalf("streamUrl = %q: адрес должен браться из Host запроса", fs.StreamURL)
	}
	resp, err := http.Get(fs.StreamURL)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("поток: %v %v", resp, err)
	}
	resp.Body.Close()
}

func TestAPIErrors(t *testing.T) {
	s, srv := apiFixture(t)
	if code := call(t, "POST", srv.URL+"/api/v1/torrents", map[string]any{"magnet": "magnet:?xt=bad"}, nil); code != 400 {
		t.Errorf("плохой magnet: %d", code)
	}
	if code := call(t, "POST", srv.URL+"/api/v1/torrents", map[string]any{"oops": 1}, nil); code != 400 {
		t.Errorf("неизвестное поле: %d", code)
	}
	unknown := strings.Repeat("0", 40)
	if code := call(t, "GET", srv.URL+"/api/v1/torrents/"+unknown, nil, nil); code != 404 {
		t.Errorf("неоткрытая раздача: %d", code)
	}
	ih, _ := s.Open(t.Context(), Source{Magnet: "magnet:?xt=urn:btih:" + strings.Repeat("ef", 20)})
	if code := call(t, "POST", srv.URL+"/api/v1/torrents/"+ih.HexString()+"/files/0/prepare", struct{}{}, nil); code != http.StatusConflict {
		t.Errorf("prepare без метаинфо: %d", code)
	}
	if code := call(t, "GET", srv.URL+"/api/v1/torrents/"+ih.HexString()+"/files/0", nil, nil); code != 404 {
		t.Errorf("состояние файла без prepare: %d", code)
	}
}
