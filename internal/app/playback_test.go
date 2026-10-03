package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Плеер через всё приложение: «Состояние» знает про ffmpeg; файл медиатеки с кириллицей, пробелами, # и [] в
// имени — сведения и поток через внутренний адрес (Review Focus 4).
func TestPlaybackThroughApp(t *testing.T) {
	ff := filepath.Join("..", "..", "third_party", "ffmpeg")
	if _, err := os.Stat(filepath.Join(ff, "ffmpeg.exe")); err != nil {
		t.Skip("нет third_party/ffmpeg (не Windows)")
	}
	a := startAppWith(t, Options{Home: t.TempDir(), ListenAddr: "127.0.0.1:0", Offline: true, DownloadsDir: t.TempDir(), FFmpegDir: ff})
	base := "http://" + a.API.Addr()
	var st struct{ Transcoder bool }
	getJSON(t, base+"/api/v1/status", &st)
	if !st.Transcoder {
		t.Fatal("transcoder: false при ffmpeg в FFmpegDir")
	}
	dir := t.TempDir()
	data, _ := os.ReadFile(filepath.Join("..", "playback", "testdata", "sample.mkv"))
	film := filepath.Join(dir, "Холоп 3 [1080p] #1.mkv")
	os.WriteFile(film, data, 0o644)
	old := time.Now().Add(-time.Hour)
	os.Chtimes(film, old, old)
	b, _ := json.Marshal(map[string]any{"name": "Фильмы", "layout": "films", "folders": []string{dir}})
	resp, err := http.Post(base+"/api/v1/library/categories", "application/json", bytes.NewReader(b))
	if err != nil || resp.StatusCode != http.StatusCreated {
		t.Fatalf("категория: %v %v", resp, err)
	}
	resp.Body.Close()
	var file int64
	waitUntil(t, "файл в медиатеке", func() bool {
		a.DB.R.QueryRow(`SELECT id FROM lib_files`).Scan(&file)
		return file > 0
	})
	var info struct {
		Audio []struct{ ID int }
		Video struct{ Mime string }
	}
	getJSON(t, fmt.Sprintf("%s/api/v1/play/library/%d", base, file), &info)
	if len(info.Audio) != 2 || info.Video.Mime != "avc1.640028" {
		t.Fatalf("сведения: %+v", info)
	}
	resp, err = http.Get(fmt.Sprintf("%s/play/library/%d/stream.ts?t=4&a=1&sid=app1", base, file))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || len(body) < 10000 || resp.Header.Get("Content-Encoding") != "" {
		t.Fatalf("поток: %d %d %q", resp.StatusCode, len(body), resp.Header.Get("Content-Encoding"))
	}
}

func TestStatusWithoutFFmpeg(t *testing.T) {
	a := startAppWith(t, Options{Home: t.TempDir(), ListenAddr: "127.0.0.1:0", Offline: true, DownloadsDir: t.TempDir(), FFmpegDir: t.TempDir()})
	var st map[string]any
	getJSON(t, "http://"+a.API.Addr()+"/api/v1/status", &st)
	if st["transcoder"] != false {
		t.Errorf("transcoder без ffmpeg: %v", st["transcoder"])
	}
	resp, _ := http.Get("http://" + a.API.Addr() + "/api/v1/play/library/1")
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 503 || !strings.Contains(string(b), "Плеер в браузере недоступен") {
		t.Errorf("без ffmpeg: %d %s", resp.StatusCode, b)
	}
}
