package player

import (
	"fmt"
	"io"
	"strings"

	"github.com/gopxl/beep/v2"
	"github.com/gopxl/beep/v2/flac"
	"github.com/gopxl/beep/v2/mp3"
	"github.com/gopxl/beep/v2/vorbis"
	"github.com/gopxl/beep/v2/wav"
)

type codec int

const (
	codecUnknown codec = iota
	codecMP3
	codecFLAC
	codecVorbis
	codecWAV
	codecPCM
)

func (c codec) String() string {
	switch c {
	case codecMP3:
		return "mp3"
	case codecFLAC:
		return "flac"
	case codecVorbis:
		return "ogg/vorbis"
	case codecWAV:
		return "wav"
	case codecPCM:
		return "pcm"
	}
	return "unknown"
}

// codecForSuffix maps a file suffix reported by the server to a decoder.
// Anything not listed here needs converting first (by the server or ffmpeg).
func codecForSuffix(suffix string) codec {
	switch strings.ToLower(strings.TrimPrefix(suffix, ".")) {
	case "mp3":
		return codecMP3
	case "flac":
		return codecFLAC
	case "ogg", "oga":
		return codecVorbis
	case "wav", "wave":
		return codecWAV
	case FormatPCM:
		return codecPCM
	}
	return codecUnknown
}

// seekableWhileStreaming reports whether the decoder can be opened on a
// seekable reader without first reading the whole stream. The mp3 and vorbis
// decoders scan to the end of a seekable input to compute its length, which
// would delay playback until the download completes, so they start on a
// forward-only reader and are reopened as seekable once downloaded.
func (c codec) seekableWhileStreaming() bool {
	return c == codecFLAC || c == codecWAV || c == codecPCM
}

func decode(c codec, r io.ReadCloser) (beep.StreamSeekCloser, beep.Format, error) {
	switch c {
	case codecMP3:
		return mp3.Decode(r)
	case codecFLAC:
		return flac.Decode(r)
	case codecVorbis:
		return vorbis.Decode(r)
	case codecWAV:
		return wav.Decode(r)
	case codecPCM:
		return decodePCM(r)
	}
	return nil, beep.Format{}, fmt.Errorf("no decoder for %s", c)
}
