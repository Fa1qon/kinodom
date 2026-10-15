package torrents

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type countingReadSeeker struct {
	*bytes.Reader
	reads int
}

func (r *countingReadSeeker) Read(p []byte) (int, error) {
	r.reads++
	return r.Reader.Read(p)
}

func TestBufferedReadSeekerReusesReadAheadAndSeeks(t *testing.T) {
	source := &countingReadSeeker{Reader: bytes.NewReader([]byte("0123456789abcdef"))}
	reader := newBufferedReadSeeker(source, 8)
	buf := make([]byte, 3)
	if _, err := io.ReadFull(reader, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "012" {
		t.Fatalf("первое чтение: %q", buf)
	}
	if source.reads != 1 {
		t.Fatalf("буфер не заполнил чтение заранее: %d вызовов", source.reads)
	}
	if _, err := io.ReadFull(reader, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "345" {
		t.Fatalf("чтение из буфера: %q", buf)
	}
	if source.reads != 1 {
		t.Fatalf("данные из буфера повторно пошли в источник: %d вызовов", source.reads)
	}
	if _, err := reader.Seek(-2, io.SeekCurrent); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(reader, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "456" {
		t.Fatalf("перемотка с учётом буфера: %q", buf)
	}
}

func TestBufferedReadSeekerWorksWithHTTPRange(t *testing.T) {
	data := []byte(strings.Repeat("0123456789", 100))
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "video.mkv", time.Time{}, newBufferedReadSeeker(bytes.NewReader(data), 64))
	})
	req := httptest.NewRequest(http.MethodGet, "/video.mkv", nil)
	req.Header.Set("Range", "bytes=37-88")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusPartialContent {
		t.Fatalf("код ответа: %d", rec.Code)
	}
	if got, want := rec.Body.Bytes(), data[37:89]; !bytes.Equal(got, want) {
		t.Fatalf("range: получено %d байт, ожидалось %d", len(got), len(want))
	}
}
