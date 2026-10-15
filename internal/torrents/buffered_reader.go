package torrents

import (
	"bufio"
	"io"
)

// bufferedReadSeeker keeps the torrent reader from being called for every
// small write made by http.ServeContent or a media player. Seek resets the
// buffer and accounts for bytes already fetched but not yet consumed.
type bufferedReadSeeker struct {
	source io.ReadSeeker
	buffer *bufio.Reader
}

func newBufferedReadSeeker(source io.ReadSeeker, size int) *bufferedReadSeeker {
	return &bufferedReadSeeker{source: source, buffer: bufio.NewReaderSize(source, size)}
}

func (r *bufferedReadSeeker) Read(p []byte) (int, error) {
	return r.buffer.Read(p)
}

func (r *bufferedReadSeeker) Seek(offset int64, whence int) (int64, error) {
	if whence == io.SeekCurrent {
		// The underlying source is ahead by the unread bytes in the buffer.
		offset -= int64(r.buffer.Buffered())
	}
	pos, err := r.source.Seek(offset, whence)
	if err == nil {
		r.buffer.Reset(r.source)
	}
	return pos, err
}
