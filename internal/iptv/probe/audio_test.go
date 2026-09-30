package probe

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"kinodom/internal/iptv/m3u"
)

// tsPacket — пакет MPEG-TS 188 байт: pid, начало раздела (PUSI), полезная нагрузка, добитая 0xFF.
func tsPacket(pid int, pusi bool, payload []byte) []byte {
	p := make([]byte, 188)
	p[0] = 0x47
	p[1] = byte(pid >> 8 & 0x1F)
	if pusi {
		p[1] |= 0x40
	}
	p[2] = byte(pid)
	p[3] = 0x10 // только полезная нагрузка
	n := copy(p[4:], payload)
	for i := 4 + n; i < 188; i++ {
		p[i] = 0xFF
	}
	return p
}

// section — раздел PSI: указатель, table_id, длина, тело и 4 байта CRC (не проверяется).
func section(tableID byte, body []byte) []byte {
	l := len(body) + 4
	out := []byte{0x00, tableID, 0xB0 | byte(l>>8&0x0F), byte(l)}
	out = append(out, body...)
	return append(out, 0, 0, 0, 0)
}

// tsWith — сегмент: PAT (программа 1 → PMT на 0x100), PMT с потоками streamTypes, дальше видео.
func tsWith(streamTypes ...byte) []byte {
	pat := section(0x00, []byte{0x00, 0x01, 0xC1, 0x00, 0x00, 0x00, 0x01, 0xE1, 0x00})
	pmt := []byte{0x00, 0x01, 0xC1, 0x00, 0x00, 0xE1, 0x01, 0xF0, 0x00}
	for i, st := range streamTypes {
		pid := 0x101 + i
		pmt = append(pmt, st, 0xE0|byte(pid>>8), byte(pid), 0xF0, 0x00)
	}
	var b []byte
	b = append(b, tsPacket(0, true, pat)...)
	b = append(b, tsPacket(0x100, true, section(0x02, pmt))...)
	for range 200 {
		b = append(b, tsPacket(0x101, false, make([]byte, 184))...)
	}
	return b
}

// Звук у источника (отзыв заказчика 2026-09-30): по таблице дорожек сегмента MPEG-TS, иначе по
// CODECS мастер-плейлиста; не понять — неизвестно.
func TestAudio(t *testing.T) {
	yes, no := true, false
	cases := []struct {
		name   string
		master string // "" — без мастер-плейлиста
		seg    []byte
		want   *bool
	}{
		{"видео и AAC", "", tsWith(0x1B, 0x0F), &yes},
		{"видео и AC-3", "", tsWith(0x1B, 0x81), &yes},
		{"только видео", "", tsWith(0x1B), &no},
		{"CODECS без звука, сегмент не TS", `#EXT-X-STREAM-INF:BANDWIDTH=100000,CODECS="avc1.64001f"`, make([]byte, 30000), &no},
		{"CODECS со звуком", `#EXT-X-STREAM-INF:BANDWIDTH=100000,CODECS="avc1.64001f,mp4a.40.2"`, make([]byte, 30000), &yes},
		{"таблица дорожек сильнее CODECS", `#EXT-X-STREAM-INF:BANDWIDTH=100000,CODECS="avc1.64001f,mp4a.40.2"`, tsWith(0x1B), &no},
		{"ничего не понять", "", make([]byte, 30000), nil},
		// Звук отдельной дорожкой (EXT-X-MEDIA с URI): в сегментах видео его и не должно быть
		// (финальное ревью: ложное «без звука» у мультиязычных каналов Flussonic).
		{"звук отдельной дорожкой", `#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="aac",NAME="rus",URI="audio.m3u8"
#EXT-X-STREAM-INF:BANDWIDTH=100000,AUDIO="aac"`, tsWith(0x1B), &yes},
		{"группа AUDIO без URI — звук в сегментах", `#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="aac",NAME="rus"
#EXT-X-STREAM-INF:BANDWIDTH=100000,AUDIO="aac"`, tsWith(0x1B), &no},
	}
	for _, c := range cases {
		mux := http.NewServeMux()
		mux.HandleFunc("/master.m3u8", func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, "#EXTM3U\n"+c.master+"\nmedia.m3u8\n")
		})
		mux.HandleFunc("/media.m3u8", func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, "#EXTM3U\n#EXTINF:1,\nseg.ts\n")
		})
		mux.HandleFunc("/seg.ts", func(w http.ResponseWriter, r *http.Request) { w.Write(c.seg) })
		srv := httptest.NewServer(mux)
		url := srv.URL + "/media.m3u8"
		if c.master != "" {
			url = srv.URL + "/master.m3u8"
		}
		r := prober().Full(context.Background(), Target{URL: url})
		srv.Close()
		if !sameBool(r.Audio, c.want) {
			t.Errorf("%s: звук %v, нужно %v (%+v)", c.name, show(r.Audio), show(c.want), r)
		}
	}
}

// Живой поток MPEG-TS: звук по таблице дорожек из принятого.
func TestAudioLiveStream(t *testing.T) {
	data := tsWith(0x1B)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp2t")
		for {
			if _, err := w.Write(data); err != nil {
				return
			}
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
				return
			case <-time.After(50 * time.Millisecond):
			}
		}
	}))
	defer srv.Close()
	r := prober().Full(context.Background(), Target{URL: srv.URL + "/live"})
	if r.Kind != m3u.KindLive || r.Audio == nil || *r.Audio {
		t.Errorf("живой поток без звука: %+v", r)
	}
}

func sameBool(a, b *bool) bool { return (a == nil) == (b == nil) && (a == nil || *a == *b) }

func show(b *bool) string {
	if b == nil {
		return "неизвестно"
	}
	return fmt.Sprint(*b)
}
