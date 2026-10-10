package player

import (
	"math"
	"math/cmplx"
)

const (
	fftSize     = 1024
	vizCapacity = 2 * fftSize
	spectrumMin = 40.0    // Hz, lowest band edge
	spectrumMax = 16000.0 // Hz, highest band edge
	floorDB     = -60.0   // level 0
	ceilDB      = -6.0    // level 1 (a half-scale sine)
	tiltPerOct  = 3.0     // dB/octave boost so treble isn't always empty
)

// capture records the mono mix of frames for the visualizer. It is called
// from the audio callback, so it only copies.
func (e *Engine) capture(frames [][2]float64) {
	e.vizMu.Lock()
	for _, f := range frames {
		e.viz[e.vizPos] = (f[0] + f[1]) / 2
		e.vizPos = (e.vizPos + 1) % vizCapacity
	}
	e.vizMu.Unlock()
}

// Spectrum returns bands levels in [0,1] for the most recently played audio,
// spaced logarithmically from 40 Hz to 16 kHz.
func (e *Engine) Spectrum(bands int) []float64 {
	samples := make([]float64, fftSize)
	e.vizMu.Lock()
	for i := range samples {
		samples[i] = e.viz[(e.vizPos-fftSize+i+vizCapacity)%vizCapacity]
	}
	e.vizMu.Unlock()
	return spectrumBands(samples, float64(outputRate), bands)
}

// spectrumBands computes log-spaced band levels in [0,1] from len(samples)
// (a power of two) mono samples.
func spectrumBands(samples []float64, rate float64, bands int) []float64 {
	out := make([]float64, bands)
	if bands <= 0 {
		return out
	}
	n := len(samples)
	buf := make([]complex128, n)
	for i, s := range samples {
		hann := 0.5 - 0.5*math.Cos(2*math.Pi*float64(i)/float64(n-1))
		buf[i] = complex(s*hann, 0)
	}
	fft(buf)

	binHz := rate / float64(n)
	ratio := spectrumMax / spectrumMin
	// A Hann-windowed sine of amplitude A peaks at A*n/4.
	norm := float64(n) / 4
	for k := range out {
		lo := spectrumMin * math.Pow(ratio, float64(k)/float64(bands))
		hi := spectrumMin * math.Pow(ratio, float64(k+1)/float64(bands))
		// Non-overlapping bin ranges; narrow low bands get at least one bin.
		b0 := max(1, int(math.Round(lo/binHz)))
		b1 := min(max(b0+1, int(math.Round(hi/binHz))), n/2)
		peak := 0.0
		for b := b0; b < b1; b++ {
			peak = max(peak, cmplx.Abs(buf[b]))
		}
		db := 20*math.Log10(peak/norm+1e-12) + tiltPerOct*math.Log2(math.Sqrt(lo*hi)/1000)
		out[k] = max(0, min(1, (db-floorDB)/(ceilDB-floorDB)))
	}
	return out
}

// fft is an in-place iterative radix-2 FFT; len(a) must be a power of two.
func fft(a []complex128) {
	n := len(a)
	for i, j := 1, 0; i < n; i++ {
		bit := n >> 1
		for ; j&bit != 0; bit >>= 1 {
			j ^= bit
		}
		j ^= bit
		if i < j {
			a[i], a[j] = a[j], a[i]
		}
	}
	for size := 2; size <= n; size <<= 1 {
		step := cmplx.Exp(complex(0, -2*math.Pi/float64(size)))
		half := size / 2
		for start := 0; start < n; start += size {
			w := complex(1, 0)
			for k := 0; k < half; k++ {
				u, v := a[start+k], a[start+k+half]*w
				a[start+k], a[start+k+half] = u+v, u-v
				w *= step
			}
		}
	}
}
