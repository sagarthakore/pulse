package player

import (
	"errors"
	"io"

	"github.com/gopxl/beep/v2"
)

const pcmFrameBytes = 4 // 16-bit stereo

// pcmDecoder decodes FormatPCM: headerless s16le stereo at 44.1 kHz. Unlike
// a WAV header written to a pipe, it needs no length up front, so it suits
// ffmpeg output whose size is only known once conversion finishes.
type pcmDecoder struct {
	r   io.ReadCloser
	pos int // frames
	buf []byte
	err error
}

func decodePCM(r io.ReadCloser) (beep.StreamSeekCloser, beep.Format, error) {
	return &pcmDecoder{r: r}, beep.Format{SampleRate: outputRate, NumChannels: 2, Precision: 2}, nil
}

func (d *pcmDecoder) Stream(samples [][2]float64) (int, bool) {
	if d.err != nil {
		return 0, false
	}
	need := len(samples) * pcmFrameBytes
	if cap(d.buf) < need {
		d.buf = make([]byte, need)
	}
	buf := d.buf[:need]
	n, err := io.ReadFull(d.r, buf)
	frames := n / pcmFrameBytes
	for i := 0; i < frames; i++ {
		b := buf[i*pcmFrameBytes:]
		samples[i][0] = float64(int16(uint16(b[0])|uint16(b[1])<<8)) / (1 << 15)
		samples[i][1] = float64(int16(uint16(b[2])|uint16(b[3])<<8)) / (1 << 15)
	}
	d.pos += frames
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		d.err = err
	}
	if err != nil && frames == 0 {
		return 0, false
	}
	return frames, true
}

func (d *pcmDecoder) Err() error { return d.err }

// Len is unknown while streaming; callers use the track's tagged duration.
func (d *pcmDecoder) Len() int { return 0 }

func (d *pcmDecoder) Position() int { return d.pos }

func (d *pcmDecoder) Seek(p int) error {
	s, ok := d.r.(io.Seeker)
	if !ok {
		return errors.New("pcm: source is not seekable")
	}
	if _, err := s.Seek(int64(p)*pcmFrameBytes, io.SeekStart); err != nil {
		return err
	}
	d.pos = p
	return nil
}

func (d *pcmDecoder) Close() error { return d.r.Close() }
