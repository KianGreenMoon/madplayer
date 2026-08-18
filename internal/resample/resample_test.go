package resample

// What a resampler has to be good at is not visible in a diff, so it is
// measured: a pure tone in, the same tone out, and the error between them.
// The ideal answer for a sine is known analytically at every output instant,
// which makes the error exact — no reference converter, no alignment, no
// judgement.

import (
	"math"
	"testing"
)

// snr resamples a tone of frequency f and returns how far the error sits
// below the signal, in dB. The first and last samples are skipped: the
// kernel is truncated at the edges, which is not what steady playback hears.
func snr(t *testing.T, in, out int, f float64) float64 {
	t.Helper()
	src := make([]float64, in*2)
	for i := range src {
		src[i] = 0.9 * math.Sin(2*math.Pi*f*float64(i)/float64(in))
	}
	got := New(in, out).Write(src, nil)
	var sigSq, errSq float64
	for i := 2000; i < len(got)-2000; i++ {
		ideal := 0.9 * math.Sin(2*math.Pi*f*float64(i)/float64(out))
		d := got[i] - ideal
		sigSq += ideal * ideal
		errSq += d * d
	}
	if errSq == 0 {
		return math.Inf(1)
	}
	return 10 * math.Log10(sigSq/errSq)
}

// The rate every phone forces on 44.1 kHz music, and the one case that has to
// be inaudible. The floor is far below what was measured (94–110 dB across
// the band) so that ordinary numeric drift does not fail the build, but well
// above what an interpolator without an anti-imaging filter can reach: beep's
// Lagrange at quality 8, the converter this replaced, answers the same tones
// at 30 dB (15 kHz) and 11 dB (19 kHz).
func TestUpsamplingIsInaudible(t *testing.T) {
	const floor = 80.0
	for _, f := range []float64{100, 1000, 5000, 10000, 15000} {
		if got := snr(t, 44100, 48000, f); got < floor {
			t.Errorf("44100 → 48000 at %.0f Hz: error only %.1f dB down, want at least %.0f", f, got, floor)
		} else {
			t.Logf("44100 → 48000 at %5.0f Hz: %6.1f dB", f, got)
		}
	}
}

// Downsampling is the fingerprinter's direction, and there the passband
// narrows to the OUTPUT Nyquist — so the tones tested are the ones that
// survive it.
func TestDownsamplingIsClean(t *testing.T) {
	const floor = 80.0
	for _, f := range []float64{100, 1000, 4000} {
		if got := snr(t, 44100, 11025, f); got < floor {
			t.Errorf("44100 → 11025 at %.0f Hz: error only %.1f dB down, want at least %.0f", f, got, floor)
		} else {
			t.Logf("44100 → 11025 at %5.0f Hz: %6.1f dB", f, got)
		}
	}
}

// Content above the output's Nyquist must be REMOVED, not folded back into
// the band as an alias — the one thing an interpolator gets wrong and a
// filter gets right.
func TestAboveNyquistIsFilteredNotAliased(t *testing.T) {
	const in, out = 44100, 11025
	src := make([]float64, in)
	for i := range src {
		src[i] = math.Sin(2 * math.Pi * 8000 * float64(i) / float64(in)) // above 5512.5
	}
	got := New(in, out).Write(src, nil)
	var peak float64
	for _, v := range got[1000 : len(got)-1000] {
		peak = math.Max(peak, math.Abs(v))
	}
	if peak > 0.01 {
		t.Errorf("an 8 kHz tone leaves %.4f behind at 11025 Hz, want it filtered away", peak)
	}
}

// The output count follows the ratio: audio does not gain or lose time.
func TestLengthFollowsTheRatio(t *testing.T) {
	r := New(44100, 48000)
	got := len(r.Flush(r.Write(make([]float64, 44100), nil)))
	if want := 48000; math.Abs(float64(got-want)) > float64(r.Latency()) {
		t.Errorf("one second of 44100 became %d samples at 48000, want %d ± %d", got, want, r.Latency())
	}
	if in, out := r.Ratio(); in != 147 || out != 160 {
		t.Errorf("44100:48000 reduced to %d:%d, want 147:160", in, out)
	}
}

// Input arrives in whatever sizes a decoder hands over, and the answer must
// not depend on them.
func TestChunkingDoesNotChangeTheAnswer(t *testing.T) {
	src := make([]float64, 30000)
	for i := range src {
		src[i] = math.Sin(2*math.Pi*1234*float64(i)/44100) * 0.8
	}
	whole := New(44100, 48000).Write(src, nil)

	piece := New(44100, 48000)
	var got []float64
	for i := 0; i < len(src); {
		n := min(1+i%777, len(src)-i)
		got = piece.Write(src[i:i+n], got)
		i += n
	}
	if len(got) != len(whole) {
		t.Fatalf("chunked gave %d samples, whole gave %d", len(got), len(whole))
	}
	for i := range whole {
		if got[i] != whole[i] {
			t.Fatalf("sample %d differs: chunked %v, whole %v", i, got[i], whole[i])
		}
	}
}
