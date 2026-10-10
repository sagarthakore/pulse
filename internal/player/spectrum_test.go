package player

import (
	"math"
	"math/cmplx"
	"testing"
)

func TestFFTMatchesDFT(t *testing.T) {
	const n = 16
	in := make([]complex128, n)
	for i := range in {
		in[i] = complex(math.Sin(float64(i)*0.7)+0.3*float64(i%3), 0)
	}
	got := append([]complex128(nil), in...)
	fft(got)
	for k := 0; k < n; k++ {
		var want complex128
		for j := 0; j < n; j++ {
			want += in[j] * cmplx.Exp(complex(0, -2*math.Pi*float64(j*k)/n))
		}
		if cmplx.Abs(got[k]-want) > 1e-9 {
			t.Fatalf("bin %d = %v, want %v", k, got[k], want)
		}
	}
}

func TestSpectrumFindsTone(t *testing.T) {
	const bands = 24
	samples := make([]float64, fftSize)
	for i := range samples {
		samples[i] = 0.5 * math.Sin(2*math.Pi*1000*float64(i)/44100)
	}
	levels := spectrumBands(samples, 44100, bands)

	// The band containing 1 kHz should be the loudest and near full scale.
	ratio := spectrumMax / spectrumMin
	want := int(float64(bands) * math.Log(1000/spectrumMin) / math.Log(ratio))
	best := 0
	for i, l := range levels {
		if l > levels[best] {
			best = i
		}
	}
	// A pure tone may straddle two adjacent bands.
	if best < want-1 || best > want+1 || levels[best] < 0.85 {
		t.Fatalf("peak at band %d (%.2f), want band %d±1; levels %.2f", best, levels[best], want, levels)
	}
	if levels[0] > 0.2 || levels[bands-1] > 0.2 {
		t.Fatalf("energy leaked to distant bands: %.2f", levels)
	}
}

func TestSpectrumSilence(t *testing.T) {
	for i, l := range spectrumBands(make([]float64, fftSize), 44100, 16) {
		if l != 0 {
			t.Fatalf("band %d = %v for silence", i, l)
		}
	}
}

func TestEngineCapturesForVisualizer(t *testing.T) {
	e := testEngine(map[string][]byte{"a": wavBytes(44100, 3000)})
	defer e.Stop()
	e.Play(Media{Opener: testOpener, ID: "a", Suffix: "wav"})
	pull(t, e, isEnded)
	if e.vizPos != 3000%vizCapacity {
		t.Fatalf("captured up to %d, want %d (played frames only)", e.vizPos, 3000%vizCapacity)
	}

	// Feed a 4 kHz tone through the capture path and find it in the output.
	tone := make([][2]float64, fftSize)
	for i := range tone {
		v := 0.5 * math.Sin(2*math.Pi*4000*float64(i)/44100)
		tone[i] = [2]float64{v, v}
	}
	e.capture(tone)
	levels := e.Spectrum(16)
	best := 0
	for i, l := range levels {
		if l > levels[best] {
			best = i
		}
	}
	if levels[best] < 0.85 || best < 10 || best > 12 {
		t.Fatalf("4 kHz tone peaked at band %d (%.2f): %.2f", best, levels[best], levels)
	}
}
