package player

import (
	"sync"
	"time"

	"github.com/gopxl/beep/v2"
)

// ring is a fixed-capacity FIFO of decoded stereo frames at the output sample
// rate. The decoder goroutine writes into it and the audio callback reads from
// it; neither side ever blocks on the other, so a slow network can only cause
// silence (an underrun), never a stalled audio thread.
type ring struct {
	mu   sync.Mutex
	buf  [][2]float64
	r, n int

	eof bool  // writer has produced its last frame
	err error // set when the writer stopped because of an error

	rate   beep.SampleRate
	base   time.Duration // track position of the first frame after a reset
	played int64         // frames consumed since the last reset
}

func newRing(capacity int, rate beep.SampleRate) *ring {
	return &ring{buf: make([][2]float64, capacity), rate: rate}
}

// write copies as many frames as fit and returns how many were written.
func (q *ring) write(src [][2]float64) int {
	q.mu.Lock()
	defer q.mu.Unlock()
	written := 0
	for written < len(src) && q.n < len(q.buf) {
		w := (q.r + q.n) % len(q.buf)
		end := len(q.buf)
		if q.r > w {
			end = q.r
		}
		c := copy(q.buf[w:end], src[written:])
		written += c
		q.n += c
	}
	return written
}

// read fills dst with buffered frames. It reports how many frames were
// copied and whether the stream has ended (writer finished and drained).
func (q *ring) read(dst [][2]float64) (n int, ended bool, err error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for n < len(dst) && q.n > 0 {
		end := q.r + q.n
		if end > len(q.buf) {
			end = len(q.buf)
		}
		c := copy(dst[n:], q.buf[q.r:end])
		n += c
		q.n -= c
		q.r = (q.r + c) % len(q.buf)
	}
	q.played += int64(n)
	return n, q.eof && q.n == 0, q.err
}

// finish marks the end of the stream; err is nil for a clean end.
func (q *ring) finish(err error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.eof = true
	if err != nil && q.err == nil {
		q.err = err
	}
}

// reset discards buffered frames after a seek to base.
func (q *ring) reset(base time.Duration) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.r, q.n = 0, 0
	q.eof, q.err = false, nil
	q.base = base
	q.played = 0
}

// state returns the playback position plus buffering information.
func (q *ring) state() (pos time.Duration, buffered int, eof bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.base + q.rate.D(int(q.played)), q.n, q.eof
}

func (q *ring) full() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.n == len(q.buf)
}
