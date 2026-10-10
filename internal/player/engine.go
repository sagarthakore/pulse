// Package player streams and plays audio using a pure-Go audio stack
// (beep + oto), so no external player is required. Where the audio comes
// from is up to each track's Opener (a Navidrome server, a local folder, ...).
package player

import (
	"context"
	"fmt"
	"io"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gopxl/beep/v2"
	"github.com/gopxl/beep/v2/speaker"
)

const (
	outputRate      = beep.SampleRate(44100)
	ringSeconds     = 3
	decodeChunk     = 2048
	resampleQuality = 4
)

// Media describes a track to play.
type Media struct {
	ID       string
	Suffix   string
	Duration time.Duration
	// Opener opens the track's audio; each music library provides its own.
	Opener Opener
}

// Opener opens the audio stream for a track.
type Opener interface {
	Open(ctx context.Context, m Media) (Source, error)
}

// Source is an opened audio stream.
type Source struct {
	Body io.ReadCloser
	// Size in bytes; <=0 if unknown. May be an estimate.
	Size int64
	// Format of Body: "mp3", "flac", "ogg", "wav" or FormatPCM.
	Format string
}

// FormatPCM is raw signed 16-bit little-endian stereo PCM at 44.1 kHz, as
// produced by e.g. `ffmpeg -f s16le -ar 44100 -ac 2`.
const FormatPCM = "pcm"

// PCMRate is the sample rate FormatPCM streams must use.
const PCMRate = int(outputRate)

// CanDecode reports whether files with this suffix can be decoded natively.
func CanDecode(suffix string) bool {
	c := codecForSuffix(suffix)
	return c != codecUnknown && c != codecPCM
}

// Options configures the engine.
type Options struct {
	// Volume is the initial volume, 0-100.
	Volume int
}

// EventKind identifies an Event.
type EventKind int

const (
	// EventEnded: the current track finished and nothing was preloaded.
	// Err is non-nil if playback stopped because of an error.
	EventEnded EventKind = iota
	// EventAdvanced: the current track finished and the preloaded track
	// started gaplessly. ID is the new track.
	EventAdvanced
	// EventNotice: a non-fatal message for the user (e.g. seek refused).
	EventNotice
)

// Event is sent to the UI on the channel returned by Events.
type Event struct {
	Kind EventKind
	ID   string
	Err  error
}

// Status is a snapshot of the engine state.
type Status struct {
	ID         string // empty when stopped
	Paused     bool
	Buffering  bool
	Position   time.Duration
	Duration   time.Duration
	Downloaded int64
	Size       int64 // <=0 if unknown
	Complete   bool  // download finished
	Codec      string
	SampleRate int
	PreloadID  string
	Volume     int
}

// Engine plays one track at a time with an optional preloaded successor.
type Engine struct {
	mu     sync.Mutex
	cur    *track
	next   *track
	paused bool
	volume int
	gain   float64

	events chan Event

	// Recently played samples (mono) for the spectrum visualizer.
	vizMu  sync.Mutex
	viz    [vizCapacity]float64
	vizPos int
}

// New initialises the audio device and returns a ready engine.
func New(opts Options) (*Engine, error) {
	if err := speaker.Init(outputRate, outputRate.N(100*time.Millisecond)); err != nil {
		return nil, fmt.Errorf("initialising audio output: %w", err)
	}
	e := &Engine{events: make(chan Event, 64)}
	e.setVolumeLocked(opts.Volume)
	speaker.Play(e)
	return e, nil
}

// Events delivers track transitions and notices.
func (e *Engine) Events() <-chan Event { return e.events }

func (e *Engine) emit(ev Event) {
	select {
	case e.events <- ev:
	default:
	}
}

// Close stops playback and releases the audio device.
func (e *Engine) Close() {
	e.Stop()
	speaker.Clear()
	speaker.Close()
}

// Play starts m immediately, replacing whatever is playing. If m is the
// preloaded track its buffered audio is reused.
func (e *Engine) Play(m Media) {
	e.mu.Lock()
	var t, stale *track
	if e.next != nil && e.next.media.ID == m.ID {
		t, e.next = e.next, nil
	} else {
		// A preload for some other track must not be swapped in later.
		stale, e.next = e.next, nil
		t = e.startTrack(m)
	}
	old := e.cur
	e.cur = t
	e.paused = false
	e.mu.Unlock()
	for _, x := range []*track{old, stale} {
		if x != nil {
			x.close()
		}
	}
}

// Preload starts buffering m so it can follow the current track gaplessly.
func (e *Engine) Preload(m Media) {
	e.mu.Lock()
	if e.next != nil && e.next.media.ID == m.ID {
		e.mu.Unlock()
		return
	}
	old := e.next
	e.next = e.startTrack(m)
	e.mu.Unlock()
	if old != nil {
		old.close()
	}
}

// ClearPreload drops the preloaded track, if any.
func (e *Engine) ClearPreload() {
	e.mu.Lock()
	old := e.next
	e.next = nil
	e.mu.Unlock()
	if old != nil {
		old.close()
	}
}

// Stop halts playback and drops the preloaded track.
func (e *Engine) Stop() {
	e.mu.Lock()
	cur, next := e.cur, e.next
	e.cur, e.next = nil, nil
	e.paused = false
	e.mu.Unlock()
	if cur != nil {
		cur.close()
	}
	if next != nil {
		next.close()
	}
}

// TogglePause pauses or resumes and returns the new paused state.
func (e *Engine) TogglePause() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cur == nil {
		return false
	}
	e.paused = !e.paused
	return e.paused
}

// Seek moves the current track to position d.
func (e *Engine) Seek(d time.Duration) {
	e.mu.Lock()
	t := e.cur
	e.mu.Unlock()
	if t != nil {
		t.requestSeek(d)
	}
}

// SetVolume sets the volume (0-100) and returns the clamped value.
func (e *Engine) SetVolume(v int) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.setVolumeLocked(v)
}

func (e *Engine) setVolumeLocked(v int) int {
	v = max(0, min(100, v))
	e.volume = v
	// A cubic curve tracks perceived loudness far better than linear gain.
	e.gain = math.Pow(float64(v)/100, 3)
	return v
}

// Status returns a snapshot of the engine state.
func (e *Engine) Status() Status {
	e.mu.Lock()
	cur, next, paused, vol := e.cur, e.next, e.paused, e.volume
	e.mu.Unlock()

	s := Status{Paused: paused, Volume: vol}
	if next != nil {
		s.PreloadID = next.media.ID
	}
	if cur == nil {
		return s
	}
	s.ID = cur.media.ID
	s.Duration = cur.media.Duration
	pos, buffered, eof := cur.ring.state()
	s.Position = pos
	if cur.seeking.Load() {
		s.Position = time.Duration(cur.seekTarget.Load())
	}
	s.Buffering = !paused && buffered == 0 && !eof
	if dl := cur.dl.Load(); dl != nil {
		s.Downloaded, s.Size, s.Complete = dl.progress()
	}
	if info := cur.info.Load(); info != nil {
		s.Codec = info.codec.String()
		s.SampleRate = int(info.rate)
		if s.Duration <= 0 && info.length > 0 {
			s.Duration = info.rate.D(info.length)
		}
	}
	return s
}

// Stream implements beep.Streamer; it is the audio callback.
func (e *Engine) Stream(samples [][2]float64) (int, bool) {
	e.mu.Lock()
	cur, paused, gain := e.cur, e.paused, e.gain
	e.mu.Unlock()

	filled := 0
	if cur != nil && !paused {
		for filled < len(samples) && cur != nil {
			n, ended, err := cur.ring.read(samples[filled:])
			filled += n
			if !ended {
				break
			}
			cur = e.advance(cur, err)
		}
	}
	for i := filled; i < len(samples); i++ {
		samples[i] = [2]float64{}
	}
	if filled > 0 {
		e.capture(samples[:filled]) // before gain: the visualizer ignores volume
	}
	if gain != 1 {
		for i := range samples[:filled] {
			samples[i][0] *= gain
			samples[i][1] *= gain
		}
	}
	return len(samples), true
}

// advance is called from the audio callback when finished has drained. It
// swaps in the preloaded track (if any) and returns the new current track.
func (e *Engine) advance(finished *track, err error) *track {
	e.mu.Lock()
	if e.cur != finished { // replaced concurrently by Play/Stop
		cur := e.cur
		e.mu.Unlock()
		return cur
	}
	var ev Event
	if e.next != nil && err == nil {
		e.cur, e.next = e.next, nil
		ev = Event{Kind: EventAdvanced, ID: e.cur.media.ID}
	} else {
		e.cur = nil
		ev = Event{Kind: EventEnded, ID: finished.media.ID, Err: err}
	}
	cur := e.cur
	e.mu.Unlock()
	go finished.close()
	e.emit(ev)
	return cur
}

// Err implements beep.Streamer.
func (e *Engine) Err() error { return nil }

func (e *Engine) startTrack(m Media) *track {
	ctx, cancel := context.WithCancel(context.Background())
	t := &track{
		media:   m,
		ctx:     ctx,
		cancel:  cancel,
		ring:    newRing(outputRate.N(ringSeconds*time.Second), outputRate),
		seekReq: make(chan time.Duration, 1),
	}
	go t.run(e)
	return t
}

// track is one stream being downloaded, decoded and buffered.
type track struct {
	media  Media
	ctx    context.Context
	cancel context.CancelFunc
	ring   *ring

	dl   atomic.Pointer[download]
	info atomic.Pointer[streamInfo]

	seekReq    chan time.Duration
	seeking    atomic.Bool
	seekTarget atomic.Int64
}

type streamInfo struct {
	codec  codec
	rate   beep.SampleRate
	length int // frames at rate, 0 if unknown
}

func (t *track) close() { t.cancel() }

// requestSeek queues a seek; only the most recent request is kept. It must
// only be called from a single goroutine (the UI).
func (t *track) requestSeek(d time.Duration) {
	if d < 0 {
		d = 0
	}
	t.seekTarget.Store(int64(d))
	t.seeking.Store(true)
	select {
	case <-t.seekReq:
	default:
	}
	t.seekReq <- d
}

// run downloads, decodes and resamples the track into the ring buffer until
// the track is closed.
func (t *track) run(e *Engine) {
	if t.media.Opener == nil {
		t.ring.finish(fmt.Errorf("no opener for track %s", t.media.ID))
		return
	}
	src0, err := t.media.Opener.Open(t.ctx, t.media)
	if err != nil {
		if t.ctx.Err() == nil {
			t.ring.finish(err)
		}
		return
	}
	c := codecForSuffix(src0.Format)
	if c == codecUnknown {
		src0.Body.Close()
		t.ring.finish(fmt.Errorf("can't decode %q audio", src0.Format))
		return
	}
	dl := newDownload(src0.Body, src0.Size)
	t.dl.Store(dl)
	defer dl.close()

	seekable := c.seekableWhileStreaming()
	open := func(seekable bool) (beep.StreamSeekCloser, beep.Format, error) {
		if seekable {
			return decode(c, dl.reader())
		}
		return decode(c, io.NopCloser(dl.reader()))
	}

	dec, fmtInfo, err := open(seekable)
	if err != nil {
		if t.ctx.Err() == nil {
			t.ring.finish(fmt.Errorf("decoding %s: %w", c, err))
		}
		return
	}
	defer func() { dec.Close() }()
	t.setInfo(c, fmtInfo, dec, seekable)

	resample := func(s beep.Streamer, f beep.Format) beep.Streamer {
		if f.SampleRate == outputRate {
			return s
		}
		return beep.Resample(resampleQuality, f.SampleRate, outputRate, s)
	}
	src := resample(dec, fmtInfo)

	chunk := make([][2]float64, decodeChunk)
	var pending [][2]float64
	srcDone := false
	var srcErr error

	notice := func(err error) {
		e.emit(Event{Kind: EventNotice, ID: t.media.ID, Err: err})
	}
	doSeek := func(target time.Duration) {
		defer t.seeking.Store(false)
		if !seekable {
			if !dl.isDone() {
				notice(fmt.Errorf("can't seek in %s until the track has finished downloading", c))
				return
			}
			// The whole file is in memory now, so reopen it seekable.
			nd, nf, err := open(true)
			if err != nil {
				notice(fmt.Errorf("seek: %w", err))
				return
			}
			dec.Close()
			dec, fmtInfo, seekable = nd, nf, true
			t.setInfo(c, fmtInfo, dec, seekable)
		}
		p := fmtInfo.SampleRate.N(target)
		if l := dec.Len(); l > 0 && p >= l {
			p = l - 1
		}
		p = max(p, 0)
		if err := dec.Seek(p); err != nil {
			notice(fmt.Errorf("seek: %w", err))
			return
		}
		src = resample(dec, fmtInfo)
		pending, srcDone, srcErr = nil, false, nil
		t.ring.reset(fmtInfo.SampleRate.D(p))
	}

	idle := time.NewTicker(15 * time.Millisecond)
	defer idle.Stop()

	for {
		select {
		case <-t.ctx.Done():
			return
		case target := <-t.seekReq:
			doSeek(target)
			continue
		default:
		}

		if len(pending) > 0 {
			w := t.ring.write(pending)
			pending = pending[w:]
			if len(pending) > 0 {
				select {
				case <-t.ctx.Done():
					return
				case target := <-t.seekReq:
					doSeek(target)
				case <-idle.C:
				}
				continue
			}
		}

		if srcDone {
			t.ring.finish(srcErr)
			select {
			case <-t.ctx.Done():
				return
			case target := <-t.seekReq:
				doSeek(target)
			}
			continue
		}

		n, ok := src.Stream(chunk)
		pending = chunk[:n]
		if !ok {
			srcDone = true
			if t.ctx.Err() == nil {
				srcErr = src.Err()
			}
		}
	}
}

func (t *track) setInfo(c codec, f beep.Format, dec beep.StreamSeekCloser, seekable bool) {
	info := &streamInfo{codec: c, rate: f.SampleRate}
	if seekable {
		info.length = dec.Len()
	}
	t.info.Store(info)
}
