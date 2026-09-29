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

	"kinodom/internal/player"
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

// Плейлист файла для плеера на другом устройстве: название — имя файла, адрес потока — с хостом, по
// которому спросили; файл сохраняется как «<название>.m3u8» (основная спека, раздел 14).
func TestM3URoute(t *testing.T) {
	s, srv := apiFixture(t)
	mi, _ := torrenttest.MakeTorrent(t, t.TempDir(), "Сезон", 64<<10,
		torrenttest.File{Path: "Серия 1.mkv", Size: 100_000}, torrenttest.File{Path: "Серия 2.mkv", Size: 100_000})
	ih, err := s.Open(t.Context(), Source{Torrent: torrentBytes(t, mi)})
	must(t, err)
	tt, _ := s.Engine().Client().Torrent(ih)
	i := fileIndex(t, tt, "Серия 2.mkv")
	resp, err := http.Get(fmt.Sprintf("%s/m3u/%s/%d.m3u8", srv.URL, ih.HexString(), i))
	must(t, err)
	defer resp.Body.Close()
	var body bytes.Buffer
	body.ReadFrom(resp.Body)
	want := fmt.Sprintf("#EXTM3U\n#EXTINF:-1,Серия 2\n%s/stream/%s/%d/", srv.URL, ih.HexString(), i)
	if resp.StatusCode != 200 || !strings.HasPrefix(body.String(), want) ||
		!strings.Contains(resp.Header.Get("Content-Disposition"), ".m3u8") {
		t.Fatalf("%d %q %s", resp.StatusCode, body.String(), resp.Header.Get("Content-Disposition"))
	}
	resp2, err := http.Get(fmt.Sprintf("%s/m3u/%s/99.m3u8", srv.URL, ih.HexString()))
	must(t, err)
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotFound {
		t.Fatalf("нет файла: %d", resp2.StatusCode)
	}
}

// «Смотреть» через API: файл хранится и в фокусе; в ответе — поток, .m3u8 и, только запросу с этого
// ПК, ссылка kinodom:// на 127.0.0.1 и порт API (спека этапа 7, раздел 5.5).
func TestWatchRoute(t *testing.T) {
	s := newTestService(t)
	mux := http.NewServeMux()
	s.Register(testRouter{mux})
	mi, _ := torrenttest.MakeTorrent(t, t.TempDir(), "Сезон", 64<<10,
		torrenttest.File{Path: "Серия 1.mkv", Size: 100_000}, torrenttest.File{Path: "Серия 2.mkv", Size: 100_000})
	ih, err := s.Open(t.Context(), Source{Torrent: torrentBytes(t, mi)})
	must(t, err)
	tt, _ := s.Engine().Client().Torrent(ih)
	i := fileIndex(t, tt, "Серия 2.mkv")
	watch := func(remote string) watchResponse {
		req := httptest.NewRequest("POST", fmt.Sprintf("/api/v1/torrents/%s/files/%d/watch", ih.HexString(), i), nil)
		req.Host, req.RemoteAddr = "192.168.1.20:8090", remote
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		var out watchResponse
		if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &out) != nil {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
		return out
	}
	out := watch("192.168.1.30:50000") // телефон
	if !strings.HasPrefix(out.Play.URL, "http://192.168.1.20:8090/stream/"+ih.HexString()) || out.Play.Title != "Серия 2" ||
		out.M3UURL != fmt.Sprintf("http://192.168.1.20:8090/m3u/%s/%d.m3u8", ih.HexString(), i) || out.LaunchURL != nil {
		t.Fatalf("с телефона: %+v", out)
	}
	if st, _ := s.Status(ih); st.Focus != i {
		t.Fatalf("фокус %d", st.Focus)
	}
	out = watch("127.0.0.1:50000") // браузер на этом ПК
	if out.LaunchURL == nil {
		t.Fatal("с этого ПК нет ссылки kinodom://")
	}
	stream, title, err := player.ParseLaunch(*out.LaunchURL, 8090)
	if err != nil || !strings.HasPrefix(stream, "http://127.0.0.1:8090/stream/") || title != "Серия 2" {
		t.Fatalf("kinodom://: %q, %q, %v", stream, title, err)
	}
}
