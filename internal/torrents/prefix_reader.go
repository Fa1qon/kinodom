package torrents

import (
	"errors"
	"io"
	"os"

	"github.com/anacrolix/torrent"
)

// prefixReader serves the verified contiguous prefix straight from disk and
// switches to the torrent reader at the first missing piece. A torrent file is
// sparse, so reading it as a normal file past that boundary would return zeroes.
// Keeping the verified prefix on the disk path lets a player fill its buffer at
// disk speed while the next pieces arrive from peers.
type prefixReader struct {
	disk   *os.File
	tail   torrent.Reader
	prefix int64
	length int64
	pos    int64
	// tailReady is false after disk reads because the torrent reader's cursor
	// did not advance with them. Synchronize it once when crossing the prefix.
	tailReady bool
}

func (r *prefixReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	total := 0
	if r.pos < r.prefix {
		nwant := min(int64(len(p)), r.prefix-r.pos)
		n, err := r.disk.ReadAt(p[:nwant], r.pos)
		r.pos += int64(n)
		total += n
		r.tailReady = false
		if err != nil && n < int(nwant) {
			return total, err
		}
		if total == len(p) {
			return total, nil
		}
		// Stop exactly at the verified boundary. The next Read will switch to
		// the torrent tail; this prevents a large buffered read from waiting for
		// an unavailable peer when the HTTP Range ends inside the prefix.
		if r.pos >= r.prefix {
			return total, nil
		}
	}
	if r.pos >= r.length {
		return total, io.EOF
	}
	if !r.tailReady {
		if _, err := r.tail.Seek(r.pos, io.SeekStart); err != nil {
			return total, err
		}
		r.tailReady = true
	}
	n, err := r.tail.Read(p[total:])
	r.pos += int64(n)
	total += n
	if total > 0 {
		return total, nil
	}
	return 0, err
}

func (r *prefixReader) Seek(offset int64, whence int) (int64, error) {
	var next int64
	switch whence {
	case io.SeekStart:
		next = offset
	case io.SeekCurrent:
		next = r.pos + offset
	case io.SeekEnd:
		next = r.length + offset
	default:
		return r.pos, errors.New("неизвестный режим перемотки")
	}
	if next < 0 {
		return r.pos, errors.New("перемотка до начала файла")
	}
	// ServeContent probes the length with Seek(0, End). That probe must not seek
	// the torrent reader to the end of a partially downloaded file: doing so
	// waits for peers before headers can be sent even when the requested Range
	// lies entirely in the verified disk prefix.
	// Do not seek the torrent reader here. ServeContent probes length and Range
	// boundaries before the first Read; a tail seek at that point would wait for
	// a peer even when the request is served from the verified disk prefix. Read
	// synchronizes the torrent cursor only when it actually crosses the prefix.
	r.tailReady = false
	r.pos = next
	return r.pos, nil
}

func (r *prefixReader) Close() error {
	err := r.disk.Close()
	if tailErr := r.tail.Close(); err == nil {
		err = tailErr
	}
	return err
}
