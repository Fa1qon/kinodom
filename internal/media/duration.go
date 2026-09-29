// Package media — длительность видеофайла по заголовку (спека этапа 8, раздел 7.2): MKV и WEBM —
// Segment → Info → Duration, MP4 — moov → mvhd, AVI — avih (или dmlh у больших файлов). Нужна, чтобы
// показать, где остановились, в минутах и открыть плеер с этого места.
package media

import (
	"encoding/binary"
	"errors"
	"io"
	"math"
	"strings"
)

// ErrUnknown — длительность не найдена: формат не тот или заголовок не читается.
var ErrUnknown = errors.New("длительность файла неизвестна")

// Duration — длительность в секундах; ext — расширение файла («.mkv»).
func Duration(r io.ReaderAt, size int64, ext string) (float64, error) {
	var d float64
	var err error
	switch strings.ToLower(ext) {
	case ".mkv", ".webm":
		d, err = matroska(r, size)
	case ".mp4", ".m4v", ".mov":
		d, err = mp4(r, size)
	case ".avi":
		d, err = avi(r, size)
	default:
		return 0, ErrUnknown
	}
	if err != nil || d <= 0 || math.IsNaN(d) || math.IsInf(d, 0) {
		return 0, ErrUnknown
	}
	return d, nil
}

// --- MKV ---

// vint — число переменной длины EBML: значение без маркера длины и длина в байтах.
func vint(r io.ReaderAt, off int64, keepMarker bool) (uint64, int, error) {
	var b [8]byte
	if _, err := r.ReadAt(b[:1], off); err != nil {
		return 0, 0, err
	}
	n := 1
	for mask := byte(0x80); n <= 8 && b[0]&mask == 0; mask >>= 1 {
		n++
	}
	if n > 8 {
		return 0, 0, ErrUnknown
	}
	if n > 1 {
		if _, err := r.ReadAt(b[1:n], off+1); err != nil {
			return 0, 0, err
		}
	}
	v := uint64(b[0])
	if !keepMarker {
		v &= uint64(0xFF >> n)
	}
	for i := 1; i < n; i++ {
		v = v<<8 | uint64(b[i])
	}
	return v, n, nil
}

const (
	idSegment       = 0x18538067
	idInfo          = 0x1549A966
	idTimecodeScale = 0x2AD7B1
	idDuration      = 0x4489
	unknownSize     = -1
)

// element — id, начало данных, размер данных (unknownSize — «до конца»).
func element(r io.ReaderAt, off int64) (id uint64, data int64, size int64, err error) {
	id, n, err := vint(r, off, true)
	if err != nil {
		return 0, 0, 0, err
	}
	sz, m, err := vint(r, off+int64(n), false)
	if err != nil {
		return 0, 0, 0, err
	}
	size = int64(sz)
	if sz == (uint64(1)<<(7*m))-1 { // все единицы — размер неизвестен
		size = unknownSize
	}
	return id, off + int64(n+m), size, nil
}

// headLimit — сколько байт от начала файла смотреть в поисках Info: оно всегда в начале.
const headLimit = 4 << 20

func matroska(r io.ReaderAt, size int64) (float64, error) {
	id, data, sz, err := element(r, 0)
	if err != nil || id != 0x1A45DFA3 || sz < 0 {
		return 0, ErrUnknown
	}
	id, seg, _, err := element(r, data+sz)
	if err != nil || id != idSegment {
		return 0, ErrUnknown
	}
	for off := seg; off < size && off < headLimit; {
		id, data, sz, err := element(r, off)
		if err != nil || sz < 0 {
			return 0, ErrUnknown
		}
		if id == idInfo {
			return matroskaInfo(r, data, sz)
		}
		off = data + sz
	}
	return 0, ErrUnknown
}

func matroskaInfo(r io.ReaderAt, start, size int64) (float64, error) {
	scale := 1_000_000.0 // нс на единицу времени по умолчанию
	dur := -1.0
	for off := start; off < start+size; {
		id, data, sz, err := element(r, off)
		if err != nil || sz < 0 || sz > 8 {
			if err == nil && sz > 8 {
				off = data + sz
				continue
			}
			return 0, ErrUnknown
		}
		b := make([]byte, sz)
		if _, err := r.ReadAt(b, data); err != nil {
			return 0, ErrUnknown
		}
		switch id {
		case idTimecodeScale:
			var v uint64
			for _, x := range b {
				v = v<<8 | uint64(x)
			}
			scale = float64(v)
		case idDuration:
			switch sz {
			case 4:
				dur = float64(math.Float32frombits(binary.BigEndian.Uint32(b)))
			case 8:
				dur = math.Float64frombits(binary.BigEndian.Uint64(b))
			}
		}
		off = data + sz
	}
	if dur < 0 {
		return 0, ErrUnknown
	}
	return dur * scale / 1e9, nil
}

// --- MP4 ---

// boxAt — заголовок бокса: тип, начало данных, конец бокса.
func boxAt(r io.ReaderAt, off, end int64) (string, int64, int64, error) {
	var h [16]byte
	if _, err := r.ReadAt(h[:8], off); err != nil {
		return "", 0, 0, err
	}
	sz := int64(binary.BigEndian.Uint32(h[:4]))
	typ := string(h[4:8])
	data := off + 8
	switch sz {
	case 0:
		sz = end - off
	case 1:
		if _, err := r.ReadAt(h[8:16], off+8); err != nil {
			return "", 0, 0, err
		}
		sz = int64(binary.BigEndian.Uint64(h[8:16]))
		data = off + 16
	}
	if sz < data-off || off+sz > end {
		return "", 0, 0, ErrUnknown
	}
	return typ, data, off + sz, nil
}

func mp4(r io.ReaderAt, size int64) (float64, error) {
	for off := int64(0); off < size; {
		typ, data, next, err := boxAt(r, off, size)
		if err != nil {
			return 0, ErrUnknown
		}
		if typ == "moov" {
			for o := data; o < next; {
				t, d, n, err := boxAt(r, o, next)
				if err != nil {
					return 0, ErrUnknown
				}
				if t == "mvhd" {
					return mvhd(r, d)
				}
				o = n
			}
			return 0, ErrUnknown
		}
		off = next
	}
	return 0, ErrUnknown
}

func mvhd(r io.ReaderAt, off int64) (float64, error) {
	var b [32]byte
	if _, err := r.ReadAt(b[:], off); err != nil {
		return 0, ErrUnknown
	}
	var scale, dur float64
	if b[0] == 1 {
		scale = float64(binary.BigEndian.Uint32(b[20:24]))
		dur = float64(binary.BigEndian.Uint64(b[24:32]))
	} else {
		scale = float64(binary.BigEndian.Uint32(b[12:16]))
		dur = float64(binary.BigEndian.Uint32(b[16:20]))
	}
	if scale == 0 {
		return 0, ErrUnknown
	}
	return dur / scale, nil
}

// --- AVI ---

func avi(r io.ReaderAt, size int64) (float64, error) {
	var h [12]byte
	if _, err := r.ReadAt(h[:], 0); err != nil || string(h[0:4]) != "RIFF" || string(h[8:12]) != "AVI " {
		return 0, ErrUnknown
	}
	// Первый LIST — hdrl: avih и, у больших файлов, LIST odml с dmlh (полное число кадров).
	var c [12]byte
	if _, err := r.ReadAt(c[:], 12); err != nil || string(c[0:4]) != "LIST" || string(c[8:12]) != "hdrl" {
		return 0, ErrUnknown
	}
	end := min(20+int64(binary.LittleEndian.Uint32(c[4:8])), size)
	var usPerFrame, frames, dml uint32
	var walk func(off, end int64) error
	walk = func(off, end int64) error {
		for off+8 <= end {
			var ch [12]byte
			if _, err := r.ReadAt(ch[:8], off); err != nil {
				return err
			}
			id, sz := string(ch[0:4]), int64(binary.LittleEndian.Uint32(ch[4:8]))
			switch id {
			case "avih":
				var a [20]byte
				if _, err := r.ReadAt(a[:], off+8); err != nil {
					return err
				}
				usPerFrame, frames = binary.LittleEndian.Uint32(a[0:4]), binary.LittleEndian.Uint32(a[16:20])
			case "dmlh":
				var d [4]byte
				if _, err := r.ReadAt(d[:], off+8); err != nil {
					return err
				}
				dml = binary.LittleEndian.Uint32(d[:])
			case "LIST":
				if err := walk(off+12, min(off+8+sz, end)); err != nil {
					return err
				}
			}
			off += 8 + sz + sz%2
		}
		return nil
	}
	if err := walk(24, end); err != nil {
		return 0, ErrUnknown
	}
	if dml > frames {
		frames = dml
	}
	return float64(frames) * float64(usPerFrame) / 1e6, nil
}
