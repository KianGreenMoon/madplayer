package chroma

import "math"

// The resampler that stands where ffmpeg's swresample stands in fpcalc.
//
// It matters more than it looks. Chromaprint's own AudioProcessor cannot
// resample at all in a normal build — the internal avresample it would need is
// compiled out — so fpcalc hands it audio ffmpeg has ALREADY reduced to 11025 Hz
// mono. Everything downstream is a fixed, exact computation over those samples,
// which makes this the one place a fingerprint can drift away from fpcalc's for
// reasons other than a bug.
//
// So it is built to swresample's own design rather than to whatever is easiest:
// a Kaiser-windowed sinc, 32 taps at unity, beta 9, cutoff 0.97 — swr's
// defaults. Two deliberate differences, both in our favour:
//
//   - The ratio is kept RATIONAL. swr quantises the phase to 1/1024 of a sample
//     and interpolates between neighbouring phases; a rational ratio has an
//     exact, finite set of phases (one for 44100→11025, 147 for 48000→11025),
//     so the table is exact and small.
//   - The filter is zero-phase, so output sample n stands at input time
//     n·in/out with no group delay to compensate. Alignment against fpcalc is
//     what a fingerprint comparison is made of; a half-frame shift is a worse
//     error than any amount of filter ripple.

const (
	// targetRate is Chromaprint's sample rate. Not a choice — the note table,
	// the frame size and every trained classifier threshold assume it.
	targetRate = 11025

	// filterSize and kaiserBeta are swresample's defaults (its filter_size and
	// kaiser_beta options). filterSize counts taps at unity ratio; downsampling
	// widens the kernel by the same factor it narrows the passband.
	filterSize = 32
	kaiserBeta = 9.0

	// cutoff is swr's Kaiser-window default: the passband edge as a fraction of
	// the output Nyquist. Below 1 to leave the transition band somewhere to go.
	cutoff = 0.97
)

// resampler converts mono audio from one rate to targetRate.
//
// It is a polyphase FIR: for output n the input position is n·M/L, whose integer
// part picks the window of input samples and whose remainder picks one of L
// precomputed coefficient phases.
type resampler struct {
	in, out  int // the ratio, reduced
	half     int // taps either side of centre, in input samples
	taps     int // 2·half
	phases   [][]float64
	buf      []float64 // input, with half samples of history kept in front
	consumed int64     // input samples already dropped from the front of buf
	next     int64     // the next output sample index
}

// newResampler prepares the phase table for rate → targetRate.
func newResampler(rate int) *resampler {
	g := gcd(rate, targetRate)
	r := &resampler{in: rate / g, out: targetRate / g}

	// Downsampling narrows the passband to the OUTPUT Nyquist, and a narrower
	// passband needs a proportionally longer kernel. Upsampling leaves it alone:
	// the input's own band is already the limit.
	factor := cutoff
	if targetRate < rate {
		factor = cutoff * float64(targetRate) / float64(rate)
	}
	r.half = int(math.Ceil(filterSize / factor / 2))
	r.taps = 2 * r.half

	// One phase per distinct fractional offset. For an integer ratio that is a
	// single phase; the table is exact either way, never interpolated.
	r.phases = make([][]float64, r.out)
	for p := range r.phases {
		frac := float64(p) / float64(r.out)
		h := make([]float64, r.taps)
		var sum float64
		for j := range h {
			// u is the distance, in input samples, from the output instant to
			// the input sample this tap weights.
			u := frac + float64(r.half-1-j)
			h[j] = factor * sinc(factor*u) * kaiser(u/float64(r.half))
			sum += h[j]
		}
		// Normalise each phase to unit DC gain, as swr does. Without it the
		// windowed sinc's residual gain error rides on every sample, and the
		// error differs per phase — a slow amplitude wobble at the phase period.
		if sum != 0 {
			for j := range h {
				h[j] /= sum
			}
		}
		r.phases[p] = h
	}
	// Leading history is silence, which is also what a decoder's first sample
	// follows. Nothing here trims a codec's encoder delay: that is the decoder's
	// business, and doing it twice would shift us off fpcalc rather than onto it.
	r.buf = make([]float64, r.half)
	r.consumed = -int64(r.half)
	return r
}

// write appends input and returns every output sample that has become
// computable. The returned slice is only valid until the next call.
func (r *resampler) write(in []float64, out []float64) []float64 {
	r.buf = append(r.buf, in...)
	for {
		// The window for output n spans input [pos-half+1, pos+half]; it is
		// ready once the last of those samples has arrived.
		num := r.next * int64(r.in)
		pos := num / int64(r.out)
		if pos+int64(r.half) >= r.consumed+int64(len(r.buf)) {
			break
		}
		h := r.phases[num%int64(r.out)]
		start := int(pos - int64(r.half) + 1 - r.consumed)
		var acc float64
		for j, c := range h {
			acc += r.buf[start+j] * c
		}
		out = append(out, acc)
		r.next++
	}
	r.compact()
	return out
}

// flush pads with silence so the tail of the input is resampled too, then
// returns the remaining output. The padding is the same silence the leading
// history assumes, so the two ends are treated alike.
func (r *resampler) flush(out []float64) []float64 {
	return r.write(make([]float64, r.half), out)
}

// compact drops input no future output can reach. Without it a whole track
// accumulates in memory, which on a phone is the difference between analysing a
// library and being killed for it.
func (r *resampler) compact() {
	pos := r.next * int64(r.in) / int64(r.out)
	keepFrom := pos - int64(r.half) + 1
	drop := int(keepFrom - r.consumed)
	if drop <= 0 {
		return
	}
	if drop > len(r.buf) {
		drop = len(r.buf)
	}
	r.buf = append(r.buf[:0], r.buf[drop:]...)
	r.consumed += int64(drop)
}

// sinc is the normalised cardinal sine, sin(pi x)/(pi x).
func sinc(x float64) float64 {
	if x == 0 {
		return 1
	}
	p := math.Pi * x
	return math.Sin(p) / p
}

// kaiser evaluates the Kaiser window at r in [-1, 1], zero outside.
func kaiser(r float64) float64 {
	if r <= -1 || r >= 1 {
		return 0
	}
	return besselI0(kaiserBeta*math.Sqrt(1-r*r)) / besselI0(kaiserBeta)
}

// besselI0 is the zeroth-order modified Bessel function of the first kind, by
// its power series. It converges quickly for the arguments a Kaiser window uses
// (at most beta), and is only ever called while building the table.
func besselI0(x float64) float64 {
	sum, term := 1.0, 1.0
	half := x / 2
	for k := 1; k < 64; k++ {
		term *= half / float64(k)
		t := term * term
		sum += t
		if t < 1e-18*sum {
			break
		}
	}
	return sum
}

func gcd(a, b int) int {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}
