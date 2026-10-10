package local

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sagarthakore/pulse/internal/player"
)

// openFFmpeg converts path to raw PCM with ffmpeg and streams it to the
// player. The process is killed when ctx ends or the stream is closed.
func openFFmpeg(ctx context.Context, bin, path string, duration time.Duration) (player.Source, error) {
	cmd := exec.CommandContext(ctx, bin,
		"-nostdin", "-hide_banner", "-loglevel", "error",
		"-i", path,
		"-map", "0:a:0", "-vn",
		"-f", "s16le", "-acodec", "pcm_s16le",
		"-ar", strconv.Itoa(player.PCMRate), "-ac", "2",
		"pipe:1",
	)
	stderr := &limitedBuffer{max: 4096}
	cmd.Stderr = stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return player.Source{}, err
	}
	if err := cmd.Start(); err != nil {
		return player.Source{}, fmt.Errorf("starting ffmpeg: %w", err)
	}
	// Size is only an estimate (for the progress bar); the real length is
	// known once ffmpeg finishes.
	size := int64(duration.Seconds()*float64(player.PCMRate)) * 4
	return player.Source{Body: &ffmpegStream{out: out, cmd: cmd, stderr: stderr}, Size: size, Format: player.FormatPCM}, nil
}

type ffmpegStream struct {
	out    io.ReadCloser
	cmd    *exec.Cmd
	stderr *limitedBuffer

	waitOnce sync.Once
	waitErr  error
}

func (s *ffmpegStream) Read(p []byte) (int, error) {
	n, err := s.out.Read(p)
	if errors.Is(err, io.EOF) {
		if werr := s.wait(); werr != nil {
			msg := strings.TrimSpace(s.stderr.String())
			if msg == "" {
				msg = werr.Error()
			}
			return n, fmt.Errorf("ffmpeg: %s", msg)
		}
	}
	return n, err
}

func (s *ffmpegStream) Close() error {
	s.out.Close()
	if s.cmd.ProcessState == nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
	s.wait()
	return nil
}

func (s *ffmpegStream) wait() error {
	s.waitOnce.Do(func() { s.waitErr = s.cmd.Wait() })
	return s.waitErr
}

// limitedBuffer keeps the first max bytes written (ffmpeg's error output).
type limitedBuffer struct {
	mu  sync.Mutex
	max int
	buf bytes.Buffer
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if room := b.max - b.buf.Len(); room > 0 {
		b.buf.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

func (b *limitedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
