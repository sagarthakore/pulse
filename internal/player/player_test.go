package player

import (
	"bytes"
	"errors"
	"io"
	"sync"
	"testing"
	"time"
)

func frames(start, n int) [][2]float64 {
	out := make([][2]float64, n)
	for i := range out {
		v := float64(start + i)
		out[i] = [2]float64{v, -v}
	}
	return out
}

func TestRingWrapAround(t *testing.T) {
	q := newRing(8, outputRate)
	if w := q.write(frames(0, 6)); w != 6 {
		t.Fatalf("write = %d, want 6", w)
	}
	dst := make([][2]float64, 4)
	if n, _, _ := q.read(dst); n != 4 || dst[0][0] != 0 || dst[3][0] != 3 {
		t.Fatalf("read = %d %v", n, dst)
	}
	// 2 buffered; 6 free, wrapping around the end.
	if w := q.write(frames(6, 10)); w != 6 {
		t.Fatalf("wrapped write = %d, want 6", w)
	}
	if !q.full() {
		t.Fatal("ring should be full")
	}
	dst = make([][2]float64, 10)
	n, ended, _ := q.read(dst)
	if n != 8 || ended {
		t.Fatalf("read = %d ended=%v", n, ended)
	}
	for i := 0; i < n; i++ {
		if dst[i][0] != float64(4+i) {
			t.Fatalf("frame %d = %v, want %d", i, dst[i][0], 4+i)
		}
	}
}

func TestRingEOFAndPosition(t *testing.T) {
	q := newRing(16, outputRate)
	q.write(frames(0, 4))
	q.finish(nil)
	dst := make([][2]float64, 2)
	if _, ended, _ := q.read(dst); ended {
		t.Fatal("ended before drained")
	}
	if _, ended, _ := q.read(dst); !ended {
		t.Fatal("not ended after drain")
	}
	pos, _, _ := q.state()
	if want := outputRate.D(4); pos != want {
		t.Fatalf("position = %v, want %v", pos, want)
	}

	q.reset(30 * time.Second)
	pos, buffered, eof := q.state()
	if pos != 30*time.Second || buffered != 0 || eof {
		t.Fatalf("after reset: pos=%v buffered=%d eof=%v", pos, buffered, eof)
	}
}

func TestRingError(t *testing.T) {
	q := newRing(4, outputRate)
	boom := errors.New("boom")
	q.finish(boom)
	if _, ended, err := q.read(make([][2]float64, 1)); !ended || !errors.Is(err, boom) {
		t.Fatalf("ended=%v err=%v", ended, err)
	}
}

// slowBody delivers data in chunks, signalled by the test.
type slowBody struct {
	chunks chan []byte
	closed chan struct{}
	once   sync.Once
}

func (b *slowBody) Read(p []byte) (int, error) {
	select {
	case c, ok := <-b.chunks:
		if !ok {
			return 0, io.EOF
		}
		return copy(p, c), nil
	case <-b.closed:
		return 0, errors.New("closed")
	}
}

func (b *slowBody) Close() error {
	b.once.Do(func() { close(b.closed) })
	return nil
}

func TestDownloadReaderBlocksUntilData(t *testing.T) {
	body := &slowBody{chunks: make(chan []byte), closed: make(chan struct{})}
	d := newDownload(body, 10)
	defer d.close()

	r := d.reader()
	got := make(chan []byte)
	go func() {
		b, _ := io.ReadAll(r)
		got <- b
	}()

	body.chunks <- []byte("hello")
	select {
	case <-got:
		t.Fatal("ReadAll returned before the download finished")
	case <-time.After(50 * time.Millisecond):
	}
	body.chunks <- []byte("world")
	close(body.chunks)

	select {
	case b := <-got:
		if string(b) != "helloworld" {
			t.Fatalf("got %q", b)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reader never completed")
	}

	recv, total, done := d.progress()
	if recv != 10 || total != 10 || !done {
		t.Fatalf("progress = %d/%d done=%v", recv, total, done)
	}
}

func TestDownloadReaderSeek(t *testing.T) {
	d := newDownload(io.NopCloser(bytes.NewReader([]byte("0123456789"))), 10)
	defer d.close()
	r := d.reader()

	if _, err := r.Seek(-3, io.SeekEnd); err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(r)
	if err != nil || string(b) != "789" {
		t.Fatalf("got %q, %v", b, err)
	}
	if _, err := r.Seek(2, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 3)
	if _, err := io.ReadFull(r, buf); err != nil || string(buf) != "234" {
		t.Fatalf("got %q, %v", buf, err)
	}
}

func TestDownloadCloseUnblocksReaders(t *testing.T) {
	body := &slowBody{chunks: make(chan []byte), closed: make(chan struct{})}
	d := newDownload(body, 0)
	errc := make(chan error, 1)
	go func() {
		_, err := d.reader().Read(make([]byte, 1))
		errc <- err
	}()
	time.Sleep(20 * time.Millisecond)
	d.close()
	select {
	case err := <-errc:
		if !errors.Is(err, errClosed) {
			t.Fatalf("err = %v, want errClosed", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reader stayed blocked after close")
	}
}

func TestCodecForSuffix(t *testing.T) {
	cases := map[string]codec{
		"mp3": codecMP3, "FLAC": codecFLAC, ".ogg": codecVorbis, "wav": codecWAV,
		"m4a": codecUnknown, "opus": codecUnknown, "": codecUnknown,
	}
	for in, want := range cases {
		if got := codecForSuffix(in); got != want {
			t.Errorf("codecForSuffix(%q) = %v, want %v", in, got, want)
		}
	}
}
