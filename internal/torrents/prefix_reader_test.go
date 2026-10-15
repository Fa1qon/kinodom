package torrents

import (
	"bytes"
	"context"
	"io"
	"os"
	"testing"

	"github.com/anacrolix/torrent"
)

type prefixTestReader struct{ *bytes.Reader }

func (r *prefixTestReader) SetContext(context.Context)                           {}
func (r *prefixTestReader) ReadContext(_ context.Context, p []byte) (int, error) { return r.Read(p) }
func (r *prefixTestReader) SetReadahead(int64)                                   {}
func (r *prefixTestReader) SetReadaheadFunc(torrent.ReadaheadFunc)               {}
func (r *prefixTestReader) SetResponsive()                                       {}
func (r *prefixTestReader) Close() error                                         { return nil }

var _ torrent.Reader = (*prefixTestReader)(nil)

func TestPrefixReaderUsesDiskThenTorrent(t *testing.T) {
	disk, err := os.CreateTemp(t.TempDir(), "prefix")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := disk.WriteString("disk-"); err != nil {
		t.Fatal(err)
	}
	if _, err := disk.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	got := &prefixReader{disk: disk, tail: &prefixTestReader{bytes.NewReader([]byte("disk-torrent-tail"))}, prefix: 5, length: 16}
	defer got.Close()
	b, err := io.ReadAll(got)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "disk-torrent-tail" {
		t.Fatalf("получено %q", b)
	}
	if _, err := got.Seek(3, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	b = make([]byte, 6)
	if _, err := io.ReadFull(got, b); err != nil {
		t.Fatal(err)
	}
	if string(b) != "k-torr" {
		t.Fatalf("после перемотки получено %q", b)
	}
}

func TestPrefixReaderLengthProbeDoesNotSeekTorrentTail(t *testing.T) {
	disk, err := os.CreateTemp(t.TempDir(), "prefix")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := disk.WriteString("disk-"); err != nil {
		t.Fatal(err)
	}
	if _, err := disk.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	got := &prefixReader{disk: disk, tail: &prefixTestReader{bytes.NewReader([]byte("tail"))}, prefix: 5, length: 9}
	defer got.Close()
	if pos, err := got.Seek(0, io.SeekEnd); err != nil || pos != 9 {
		t.Fatalf("проверка длины: pos=%d err=%v", pos, err)
	}
}
