package chroma

import "math"

// A radix-2 FFT, written out rather than depended on.
//
// Chromaprint needs exactly one transform — 4096 real points, per frame — and
// wants the squared magnitude, not the spectrum. A general FFT library would be
// a dependency, a build-tag surface and an allocation policy for a page of
// arithmetic that has not changed since 1965.

// fft computes the power spectrum of a real frame in place-ish: re/im are
// scratch the caller owns, out receives size/2+1 squared magnitudes.
type fft struct {
	size    int
	re, im  []float64
	cos     []float64 // twiddles, indexed by half-size step
	sin     []float64
	reverse []int // bit-reversal permutation
}

func newFFT(size int) *fft {
	f := &fft{
		size:    size,
		re:      make([]float64, size),
		im:      make([]float64, size),
		cos:     make([]float64, size/2),
		sin:     make([]float64, size/2),
		reverse: make([]int, size),
	}
	for i := range f.cos {
		a := -2 * math.Pi * float64(i) / float64(size)
		f.cos[i], f.sin[i] = math.Cos(a), math.Sin(a)
	}
	bits := 0
	for 1<<bits < size {
		bits++
	}
	for i := range f.reverse {
		v, r := i, 0
		for b := 0; b < bits; b++ {
			r = r<<1 | v&1
			v >>= 1
		}
		f.reverse[i] = r
	}
	return f
}

// power transforms the real frame in and writes size/2+1 squared magnitudes
// into out. A real input is transformed as a complex one with zero imaginary
// part: the arithmetic is twice what a real-specific transform needs and is
// nowhere near the cost of decoding the audio it runs on.
func (f *fft) power(in, out []float64) {
	for i, r := range f.reverse {
		f.re[i], f.im[i] = in[r], 0
	}
	for width := 2; width <= f.size; width <<= 1 {
		half, step := width/2, f.size/width
		for base := 0; base < f.size; base += width {
			for k, t := 0, 0; k < half; k, t = k+1, t+step {
				i, j := base+k, base+k+half
				wr, wi := f.cos[t], f.sin[t]
				tr := f.re[j]*wr - f.im[j]*wi
				ti := f.re[j]*wi + f.im[j]*wr
				f.re[j], f.im[j] = f.re[i]-tr, f.im[i]-ti
				f.re[i], f.im[i] = f.re[i]+tr, f.im[i]+ti
			}
		}
	}
	for i := range out {
		out[i] = f.re[i]*f.re[i] + f.im[i]*f.im[i]
	}
}

// hammingWindow is the window Chromaprint applies before every transform,
// scaled the way its FFT layer scales it: by 1/INT16_MAX, because the samples
// arriving are int16 and the classifier thresholds were trained on the result.
//
// Note the size-1 denominator. It is the symmetric Hamming rather than the
// periodic one, and swapping them changes the spectrum enough to matter.
func hammingWindow(size int) []float64 {
	w := make([]float64, size)
	for i := range w {
		w[i] = (0.54 - 0.46*math.Cos(float64(i)*2*math.Pi/float64(size-1))) / math.MaxInt16
	}
	return w
}
