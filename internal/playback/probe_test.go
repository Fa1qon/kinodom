package playback

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestVideoMime(t *testing.T) {
	for _, c := range []struct {
		codec, profile string
		level          int
		pix, want      string
	}{
		{"h264", "High", 40, "yuv420p", "avc1.640028"},
		{"h264", "Main", 31, "yuv420p", "avc1.4D401F"},
		{"h264", "Constrained Baseline", 30, "yuv420p", "avc1.42E01E"},
		{"h264", "High 10", 40, "yuv420p10le", ""},
		{"h264", "High", 40, "yuv420p10le", ""},
		{"h264", "High", 0, "yuv420p", ""},
		{"hevc", "Main", 120, "yuv420p", "hvc1.1.6.L120.B0"},
		{"hevc", "Main 10", 150, "yuv420p10le", "hvc1.2.4.L150.B0"},
		{"mpeg4", "Advanced Simple Profile", 5, "yuv420p", ""},
		{"vc1", "Advanced", 3, "yuv420p", ""},
	} {
		if got := videoMime(c.codec, c.profile, c.level, c.pix); got != c.want {
			t.Errorf("%s %s %d %s: %q, ждали %q", c.codec, c.profile, c.level, c.pix, got, c.want)
		}
	}
}

// Обложка (attached_pic) — не видео; «und» — без языка; субтитры-картинки помечены; нет видео — ошибка.
func TestParseProbe(t *testing.T) {
	m, err := parseProbe([]byte(`{"streams":[
	 {"index":0,"codec_type":"video","codec_name":"mjpeg","disposition":{"attached_pic":1}},
	 {"index":1,"codec_type":"video","codec_name":"h264","profile":"High","level":41,"pix_fmt":"yuv420p","width":1920,"height":804,"disposition":{"default":1}},
	 {"index":2,"codec_type":"audio","codec_name":"eac3","channels":6,"tags":{"language":"rus","title":"Дубляж"},"disposition":{"default":1}},
	 {"index":3,"codec_type":"audio","codec_name":"ac3","channels":2,"tags":{"LANGUAGE":"und"},"disposition":{"default":0}},
	 {"index":4,"codec_type":"subtitle","codec_name":"hdmv_pgs_subtitle","tags":{"language":"eng"}},
	 {"index":5,"codec_type":"subtitle","codec_name":"subrip","tags":{"language":"rus","title":"Надписи"},"disposition":{"forced":1}}],
	 "format":{"duration":"2559.400000"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if m.Duration != 2559.4 || m.Video != (Video{ID: 1, Codec: "h264", Mime: "avc1.640029", Width: 1920, Height: 804}) {
		t.Errorf("видео: %+v %v", m.Video, m.Duration)
	}
	if len(m.Audio) != 2 || m.Audio[0] != (Track{ID: 2, Lang: "rus", Title: "Дубляж", Codec: "eac3", Channels: 6, Default: true}) ||
		m.Audio[1] != (Track{ID: 3, Codec: "ac3", Channels: 2}) {
		t.Errorf("звук: %+v", m.Audio)
	}
	if len(m.Subs) != 2 || !m.Subs[0].Image || m.Subs[0].ID != "4" || m.Subs[1].Image || !m.Subs[1].Forced || m.Subs[1].Title != "Надписи" {
		t.Errorf("субтитры: %+v", m.Subs)
	}
	if _, err := parseProbe([]byte(`{"streams":[{"index":0,"codec_type":"audio","codec_name":"mp3"}],"format":{}}`)); err == nil {
		t.Error("без видео — нет ошибки")
	}
	if _, err := parseProbe([]byte(`не json`)); err == nil {
		t.Error("мусор — нет ошибки")
	}
}

// Настоящий ffprobe из комплекта на тестовых файлах.
func TestProbeFixtures(t *testing.T) {
	tl := tools(t)
	probe := func(name string) Media {
		m, err := tl.Probe(context.Background(), filepath.Join("testdata", name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return m
	}
	m := probe("sample.mkv")
	if m.Video.ID != 0 || m.Video.Mime != "avc1.640028" || m.Video.Width != 320 || m.Duration < 11.9 || m.Duration > 12.1 {
		t.Errorf("sample.mkv видео: %+v %v", m.Video, m.Duration)
	}
	if len(m.Audio) != 2 || m.Audio[0] != (Track{ID: 1, Lang: "rus", Title: "Дубляж", Codec: "ac3", Channels: 6, Default: true}) ||
		m.Audio[1] != (Track{ID: 2, Lang: "eng", Title: "Original", Codec: "aac", Channels: 2}) {
		t.Errorf("sample.mkv звук: %+v", m.Audio)
	}
	if len(m.Subs) != 1 || m.Subs[0].ID != "3" || m.Subs[0].Title != "Надписи" || m.Subs[0].Image {
		t.Errorf("sample.mkv субтитры: %+v", m.Subs)
	}
	if v := probe("hevc.mp4").Video; !strings.HasPrefix(v.Mime, "hvc1.1.6.L") {
		t.Errorf("hevc.mp4: %+v", v)
	}
	if v := probe("hi10.mkv").Video; v.Mime != "" || v.Codec != "h264" {
		t.Errorf("hi10.mkv — браузер не покажет: %+v", v)
	}
	if v := probe("xvid.avi").Video; v.Mime != "" || v.Codec != "mpeg4" {
		t.Errorf("xvid.avi: %+v", v)
	}
	if _, err := tl.Probe(context.Background(), filepath.Join("testdata", "нет.mkv")); err == nil {
		t.Error("нет файла — нет ошибки")
	}
}
