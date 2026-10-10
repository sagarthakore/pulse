package player

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"testing"
	"time"

	"github.com/mewkiz/flac"
	"github.com/mewkiz/flac/frame"
	"github.com/mewkiz/flac/meta"
)

const amplitude = 16384 // 0.5 full scale

// wavBytes returns a 16-bit stereo PCM WAV of n frames at rate.
func wavBytes(rate, n int) []byte {
	var b bytes.Buffer
	data := n * 4
	b.WriteString("RIFF")
	binary.Write(&b, binary.LittleEndian, uint32(36+data))
	b.WriteString("WAVEfmt ")
	binary.Write(&b, binary.LittleEndian, uint32(16))
	binary.Write(&b, binary.LittleEndian, uint16(1)) // PCM
	binary.Write(&b, binary.LittleEndian, uint16(2)) // channels
	binary.Write(&b, binary.LittleEndian, uint32(rate))
	binary.Write(&b, binary.LittleEndian, uint32(rate*4))
	binary.Write(&b, binary.LittleEndian, uint16(4))
	binary.Write(&b, binary.LittleEndian, uint16(16))
	b.WriteString("data")
	binary.Write(&b, binary.LittleEndian, uint32(data))
	for i := 0; i < n; i++ {
		binary.Write(&b, binary.LittleEndian, int16(amplitude))
		binary.Write(&b, binary.LittleEndian, int16(-amplitude))
	}
	return b.Bytes()
}

// flacBytes returns a 16-bit stereo FLAC of blocks*blockSize frames at 44.1kHz.
func flacBytes(t *testing.T, blocks int) []byte {
	t.Helper()
	const blockSize = 4096
	var b bytes.Buffer
	info := &meta.StreamInfo{
		BlockSizeMin:  blockSize,
		BlockSizeMax:  blockSize,
		SampleRate:    44100,
		NChannels:     2,
		BitsPerSample: 16,
		NSamples:      uint64(blocks * blockSize),
	}
	enc, err := flac.NewEncoder(&b, info)
	if err != nil {
		t.Fatal(err)
	}
	for n := 0; n < blocks; n++ {
		left := make([]int32, blockSize)
		right := make([]int32, blockSize)
		for i := range left {
			left[i], right[i] = amplitude, -amplitude
		}
		f := &frame.Frame{
			Header: frame.Header{
				HasFixedBlockSize: true,
				BlockSize:         blockSize,
				SampleRate:        44100,
				Channels:          frame.ChannelsLR,
				BitsPerSample:     16,
				Num:               uint64(n),
			},
			Subframes: []*frame.Subframe{
				{SubHeader: frame.SubHeader{Pred: frame.PredVerbatim}, Samples: left, NSamples: blockSize},
				{SubHeader: frame.SubHeader{Pred: frame.PredVerbatim}, Samples: right, NSamples: blockSize},
			},
		}
		if err := enc.WriteFrame(f); err != nil {
			t.Fatal(err)
		}
	}
	if err := enc.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// fakeOpener serves in-memory files; the Media suffix is the stream format.
type fakeOpener map[string][]byte

func (f fakeOpener) Open(_ context.Context, m Media) (Source, error) {
	b, ok := f[m.ID]
	if !ok {
		return Source{}, errors.New("not found")
	}
	return Source{Body: io.NopCloser(bytes.NewReader(b)), Size: int64(len(b)), Format: m.Suffix}, nil
}

// testOpener is the opener of the most recent testEngine.
var testOpener fakeOpener

// testEngine builds an engine without an audio device; the test drives the
// audio callback itself.
func testEngine(files map[string][]byte) *Engine {
	testOpener = files
	e := &Engine{events: make(chan Event, 64)}
	e.setVolumeLocked(100)
	return e
}

// pull runs the audio callback until stop returns true for an event,
// returning the number of audible frames and every event seen.
func pull(t *testing.T, e *Engine, stop func(Event) bool) (int, []Event) {
	t.Helper()
	buf := make([][2]float64, 1024)
	audible := 0
	var events []Event
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		e.Stream(buf)
		got := 0
		for _, s := range buf {
			if math.Abs(s[0]) > 0.1 {
				if math.Abs(s[0]-0.5) > 0.01 || math.Abs(s[1]+0.5) > 0.01 {
					t.Fatalf("unexpected sample %v", s)
				}
				got++
			}
		}
		audible += got
		for {
			select {
			case ev := <-e.events:
				events = append(events, ev)
				if stop(ev) {
					return audible, events
				}
				continue
			default:
			}
			break
		}
		if got == 0 {
			time.Sleep(time.Millisecond)
		}
	}
	t.Fatalf("timed out; %d frames, events %+v", audible, events)
	return 0, nil
}

func isEnded(ev Event) bool { return ev.Kind == EventEnded }

func TestEnginePlaysWAVToEnd(t *testing.T) {
	e := testEngine(map[string][]byte{"a": wavBytes(44100, 44100)})
	defer e.Stop()
	e.Play(Media{Opener: testOpener, ID: "a", Suffix: "wav", Duration: time.Second})

	frames, events := pull(t, e, isEnded)
	if frames != 44100 {
		t.Fatalf("played %d frames, want 44100", frames)
	}
	if last := events[len(events)-1]; last.ID != "a" || last.Err != nil {
		t.Fatalf("end event = %+v", last)
	}
	if st := e.Status(); st.ID != "" {
		t.Fatalf("still playing %q after end", st.ID)
	}
}

func TestEngineGaplessAdvance(t *testing.T) {
	e := testEngine(map[string][]byte{
		"a": wavBytes(44100, 30000),
		"b": wavBytes(44100, 20000),
	})
	defer e.Stop()
	e.Play(Media{Opener: testOpener, ID: "a", Suffix: "wav"})
	e.Preload(Media{Opener: testOpener, ID: "b", Suffix: "wav"})

	frames, events := pull(t, e, isEnded)
	if frames != 50000 {
		t.Fatalf("played %d frames, want 50000 (no gap, nothing dropped)", frames)
	}
	if len(events) != 2 || events[0].Kind != EventAdvanced || events[0].ID != "b" || events[1].ID != "b" {
		t.Fatalf("events = %+v", events)
	}
}

func TestEnginePlayDropsStalePreload(t *testing.T) {
	e := testEngine(map[string][]byte{
		"a": wavBytes(44100, 1000),
		"b": wavBytes(44100, 1000),
		"c": wavBytes(44100, 1000),
	})
	defer e.Stop()
	e.Play(Media{Opener: testOpener, ID: "a", Suffix: "wav"})
	e.Preload(Media{Opener: testOpener, ID: "b", Suffix: "wav"})
	e.Play(Media{Opener: testOpener, ID: "c", Suffix: "wav"})
	if st := e.Status(); st.PreloadID != "" {
		t.Fatalf("stale preload %q survived Play", st.PreloadID)
	}
	_, events := pull(t, e, isEnded)
	if len(events) != 1 || events[0].ID != "c" {
		t.Fatalf("events = %+v", events)
	}
}

func TestEngineResamples(t *testing.T) {
	e := testEngine(map[string][]byte{"hi": wavBytes(48000, 48000)})
	defer e.Stop()
	e.Play(Media{Opener: testOpener, ID: "hi", Suffix: "wav"})
	frames, _ := pull(t, e, isEnded)
	if frames < 44000 || frames > 44200 {
		t.Fatalf("1s of 48kHz played as %d frames at 44.1kHz", frames)
	}
}

func TestEngineSeekFLAC(t *testing.T) {
	const blocks = 32 // 131072 frames, ~2.97s
	e := testEngine(map[string][]byte{"f": flacBytes(t, blocks)})
	defer e.Stop()
	e.Play(Media{Opener: testOpener, ID: "f", Suffix: "flac"})

	// Wait for audio to start, then seek to 2s.
	buf := make([][2]float64, 256)
	deadline := time.Now().Add(5 * time.Second)
	for e.Status().Position == 0 && time.Now().Before(deadline) {
		e.Stream(buf)
		time.Sleep(time.Millisecond)
	}
	e.Seek(2 * time.Second)
	cur := e.cur
	for cur.seeking.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if pos := e.Status().Position; pos != 2*time.Second {
		t.Fatalf("position after seek = %v", pos)
	}

	frames, _ := pull(t, e, isEnded)
	want := blocks*4096 - 88200
	if d := frames - want; d < -10 || d > 10 {
		t.Fatalf("played %d frames after seek, want ~%d", frames, want)
	}
}

func TestEngineOpenErrorEndsWithError(t *testing.T) {
	e := testEngine(nil)
	defer e.Stop()
	e.Play(Media{Opener: testOpener, ID: "missing", Suffix: "mp3"})
	_, events := pull(t, e, isEnded)
	if events[0].Err == nil {
		t.Fatalf("expected an error, got %+v", events[0])
	}
}

func TestEnginePauseOutputsSilence(t *testing.T) {
	e := testEngine(map[string][]byte{"a": wavBytes(44100, 44100)})
	defer e.Stop()
	e.Play(Media{Opener: testOpener, ID: "a", Suffix: "wav"})
	time.Sleep(50 * time.Millisecond) // let the ring fill
	if !e.TogglePause() {
		t.Fatal("expected paused")
	}
	buf := make([][2]float64, 4096)
	e.Stream(buf)
	for _, s := range buf {
		if s != [2]float64{} {
			t.Fatal("audio produced while paused")
		}
	}
	if pos := e.Status().Position; pos != 0 {
		t.Fatalf("position advanced while paused: %v", pos)
	}
}

// pcmBytes returns n frames of raw s16le stereo PCM.
func pcmBytes(n int) []byte {
	var b bytes.Buffer
	for i := 0; i < n; i++ {
		binary.Write(&b, binary.LittleEndian, int16(amplitude))
		binary.Write(&b, binary.LittleEndian, int16(-amplitude))
	}
	return b.Bytes()
}

func TestEnginePlaysPCM(t *testing.T) {
	e := testEngine(map[string][]byte{"p": pcmBytes(30000)})
	defer e.Stop()
	e.Play(Media{Opener: testOpener, ID: "p", Suffix: FormatPCM})
	if frames, _ := pull(t, e, isEnded); frames != 30000 {
		t.Fatalf("played %d frames, want 30000", frames)
	}
}

func TestEngineSeekPCM(t *testing.T) {
	e := testEngine(map[string][]byte{"p": pcmBytes(3 * 44100)})
	defer e.Stop()
	e.Play(Media{Opener: testOpener, ID: "p", Suffix: FormatPCM})
	e.Seek(2 * time.Second)
	cur := e.cur
	deadline := time.Now().Add(5 * time.Second)
	for cur.seeking.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if frames, _ := pull(t, e, isEnded); frames != 44100 {
		t.Fatalf("played %d frames after seeking to 2s of 3s, want 44100", frames)
	}
}

func TestEngineUnknownFormatEndsWithError(t *testing.T) {
	e := testEngine(map[string][]byte{"x": []byte("data")})
	defer e.Stop()
	e.Play(Media{Opener: testOpener, ID: "x", Suffix: "m4a"})
	if _, events := pull(t, e, isEnded); events[0].Err == nil {
		t.Fatal("expected an error for an undecodable format")
	}
}

func TestCanDecode(t *testing.T) {
	for suffix, want := range map[string]bool{"mp3": true, "FLAC": true, "ogg": true, "wav": true, "m4a": false, "opus": false, "pcm": false} {
		if got := CanDecode(suffix); got != want {
			t.Errorf("CanDecode(%q) = %v, want %v", suffix, got, want)
		}
	}
}

func TestVolumeCurve(t *testing.T) {
	e := testEngine(nil)
	if v := e.SetVolume(150); v != 100 || e.gain != 1 {
		t.Fatalf("clamp high: v=%d gain=%v", v, e.gain)
	}
	if v := e.SetVolume(-5); v != 0 || e.gain != 0 {
		t.Fatalf("clamp low: v=%d gain=%v", v, e.gain)
	}
	e.SetVolume(50)
	if math.Abs(e.gain-0.125) > 1e-9 {
		t.Fatalf("gain at 50%% = %v", e.gain)
	}
}
