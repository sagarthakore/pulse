package player

import (
	"errors"
	"io"
	"sync"
)

var errClosed = errors.New("download closed")

// download is an in-memory progressive download of an HTTP body. Readers
// created from it see the whole stream and block until the bytes they need
// have arrived, which lets decoders start before the download finishes.
type download struct {
	mu     sync.Mutex
	cond   *sync.Cond
	data   []byte
	total  int64 // expected size, <=0 if unknown; exact once done
	done   bool
	closed bool
	err    error
	body   io.ReadCloser
}

func newDownload(body io.ReadCloser, total int64) *download {
	d := &download{body: body, total: total}
	d.cond = sync.NewCond(&d.mu)
	if total > 0 && total < 1<<30 {
		d.data = make([]byte, 0, total)
	}
	go d.run()
	return d
}

func (d *download) run() {
	buf := make([]byte, 64*1024)
	for {
		n, err := d.body.Read(buf)
		d.mu.Lock()
		if n > 0 {
			d.data = append(d.data, buf[:n]...)
		}
		if err != nil {
			d.done = true
			d.total = int64(len(d.data))
			if !errors.Is(err, io.EOF) && !d.closed {
				d.err = err
			}
		}
		d.cond.Broadcast()
		d.mu.Unlock()
		if err != nil {
			d.body.Close()
			return
		}
	}
}

// close aborts the download and wakes any blocked readers.
func (d *download) close() {
	d.mu.Lock()
	d.closed = true
	d.cond.Broadcast()
	d.mu.Unlock()
	d.body.Close()
}

// progress reports bytes received, expected total (<=0 if unknown) and
// whether the download has completed.
func (d *download) progress() (received, total int64, done bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return int64(len(d.data)), d.total, d.done
}

func (d *download) isDone() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.done && d.err == nil
}

// reader returns a new independent ReadSeekCloser positioned at the start.
func (d *download) reader() *downloadReader {
	return &downloadReader{d: d}
}

type downloadReader struct {
	d   *download
	pos int64
}

func (r *downloadReader) Read(p []byte) (int, error) {
	d := r.d
	d.mu.Lock()
	defer d.mu.Unlock()
	for int64(len(d.data)) <= r.pos && !d.done && !d.closed {
		d.cond.Wait()
	}
	if d.closed {
		return 0, errClosed
	}
	if r.pos >= int64(len(d.data)) {
		if d.err != nil {
			return 0, d.err
		}
		return 0, io.EOF
	}
	n := copy(p, d.data[r.pos:])
	r.pos += int64(n)
	return n, nil
}

func (r *downloadReader) Seek(offset int64, whence int) (int64, error) {
	var base int64
	switch whence {
	case io.SeekStart:
	case io.SeekCurrent:
		base = r.pos
	case io.SeekEnd:
		d := r.d
		d.mu.Lock()
		// The advertised length can be an estimate (transcoded streams), so
		// only trust it outright for an unfinished download if it is known.
		for d.total <= 0 && !d.done && !d.closed {
			d.cond.Wait()
		}
		closed := d.closed
		base = d.total
		d.mu.Unlock()
		if closed {
			return 0, errClosed
		}
	default:
		return 0, errors.New("invalid whence")
	}
	pos := base + offset
	if pos < 0 {
		return 0, errors.New("negative position")
	}
	r.pos = pos
	return pos, nil
}

// Close is a no-op: the owning track closes the download itself.
func (r *downloadReader) Close() error { return nil }
