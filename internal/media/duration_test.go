package media

import (
	"bytes"
	"encoding/binary"
	"math"
	"testing"
)

// ebml — элемент EBML: id (уже с маркером длины), размер в одном байте длины (до 126 байт) или 8 байтах.
func ebml(id []byte, body []byte) []byte {
	out := append([]byte{}, id...)
	if len(body) < 127 {
		out = append(out, 0x80|byte(len(body)))
	} else {
		sz := make([]byte, 8)
		binary.BigEndian.PutUint64(sz, uint64(len(body)))
		sz[0] = 0x01
		out = append(out, sz...)
	}
	return append(out, body...)
}

func f64(v float64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, math.Float64bits(v))
	return b
}

func f32(v float32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, math.Float32bits(v))
	return b
}

// mkvFile — заголовок EBML, Segment неизвестного размера: SeekHead, Void, Info с TimecodeScale и Duration.
func mkvFile(scale []byte, dur []byte) []byte {
	var b bytes.Buffer
	b.Write(ebml([]byte{0x1A, 0x45, 0xDF, 0xA3}, ebml([]byte{0x42, 0x82}, []byte("matroska"))))
	info := ebml([]byte{0x44, 0x89}, dur)
	if scale != nil {
		info = append(ebml([]byte{0x2A, 0xD7, 0xB1}, scale), info...)
	}
	seg := append(ebml([]byte{0x11, 0x4D, 0x9B, 0x74}, make([]byte, 20)), ebml([]byte{0xEC}, make([]byte, 40))...)
	seg = append(seg, ebml([]byte{0x15, 0x49, 0xA9, 0x66}, info)...)
	b.Write([]byte{0x18, 0x53, 0x80, 0x67, 0x01, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}) // размер «неизвестен»
	b.Write(seg)
	b.Write(make([]byte, 1000)) // кластеры
	return b.Bytes()
}

func box(typ string, body []byte) []byte {
	out := make([]byte, 8)
	binary.BigEndian.PutUint32(out, uint32(8+len(body)))
	copy(out[4:], typ)
	return append(out, body...)
}

// mp4File — ftyp, mdat, moov в конце (как бывает у раздач): mvhd версии 0 или 1.
func mp4File(version byte, timescale uint32, duration uint64) []byte {
	var mvhd []byte
	if version == 0 {
		mvhd = make([]byte, 100)
		binary.BigEndian.PutUint32(mvhd[12:], timescale)
		binary.BigEndian.PutUint32(mvhd[16:], uint32(duration))
	} else {
		mvhd = make([]byte, 112)
		mvhd[0] = 1
		binary.BigEndian.PutUint32(mvhd[20:], timescale)
		binary.BigEndian.PutUint64(mvhd[24:], duration)
	}
	var b bytes.Buffer
	b.Write(box("ftyp", []byte("isom0000isom")))
	b.Write(box("mdat", make([]byte, 5000)))
	b.Write(box("moov", append(box("mvhd", mvhd), box("trak", make([]byte, 50))...)))
	return b.Bytes()
}

// aviFile — RIFF AVI: hdrl с avih (мкс на кадр, число кадров) и, для больших файлов, odml/dmlh.
func aviFile(usPerFrame, frames, dmlFrames uint32) []byte {
	le := func(v uint32) []byte { b := make([]byte, 4); binary.LittleEndian.PutUint32(b, v); return b }
	chunk := func(id string, body []byte) []byte {
		return append(append([]byte(id), le(uint32(len(body)))...), body...)
	}
	avih := make([]byte, 56)
	copy(avih[0:], le(usPerFrame))
	copy(avih[16:], le(frames))
	hdrl := append([]byte("hdrl"), chunk("avih", avih)...)
	if dmlFrames > 0 {
		hdrl = append(hdrl, chunk("LIST", append([]byte("odml"), chunk("dmlh", append(le(dmlFrames), make([]byte, 244)...))...))...)
	}
	body := append([]byte("AVI "), chunk("LIST", hdrl)...)
	body = append(body, chunk("LIST", append([]byte("movi"), make([]byte, 3000)...))...)
	return chunk("RIFF", body)
}

func TestDuration(t *testing.T) {
	cases := []struct {
		name string
		ext  string
		data []byte
		want float64
	}{
		{"MKV, масштаб по умолчанию, float64", ".mkv", mkvFile(nil, f64(5_400_000)), 5400},
		{"MKV, свой масштаб, float32", ".mkv", mkvFile([]byte{0x0F, 0x42, 0x40}, f32(3_600_000)), 3600}, // 1 000 000 нс
		{"WEBM", ".webm", mkvFile(nil, f64(1234_500)), 1234.5},
		{"MP4 v0, moov в конце", ".mp4", mp4File(0, 1000, 7_080_000), 7080},
		{"M4V v1", ".m4v", mp4File(1, 90000, 90000*2700), 2700},
		{"AVI", ".avi", aviFile(40000, 67500, 0), 2700},
		{"AVI OpenDML", ".avi", aviFile(40000, 1000, 150000), 6000},
	}
	for _, c := range cases {
		got, err := Duration(bytes.NewReader(c.data), int64(len(c.data)), c.ext)
		if err != nil || math.Abs(got-c.want) > 0.01 {
			t.Errorf("%s: %v, %v; нужно %v", c.name, got, err, c.want)
		}
	}
}

func TestDurationUnknown(t *testing.T) {
	for _, c := range []struct {
		ext  string
		data []byte
	}{
		{".mkv", []byte("не видео, а текст")},
		{".mp4", box("ftyp", []byte("isom"))},
		{".avi", []byte("RIFF")},
		{".ts", make([]byte, 1000)},
		{".mkv", mkvFile(nil, nil)[:30]},
	} {
		if d, err := Duration(bytes.NewReader(c.data), int64(len(c.data)), c.ext); err == nil {
			t.Errorf("%s: длительность %v без ошибки", c.ext, d)
		}
	}
}
