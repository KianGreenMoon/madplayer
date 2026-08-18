// Package chroma computes Chromaprint acoustic fingerprints in this process.
//
// It exists because fpcalc is a program, and a phone has nowhere to put one.
// madshare's analysis shells out to fpcalc and ffprobe, which is right for a
// server and impossible on Android: no PATH to install onto, no permission to
// execute anything the app wrote, and no process of our own to re-exec. Without
// a fingerprint a device gets no duplicate detection and — because a node that
// cannot verify what it downloads must not redistribute it — no mesh either.
//
// This is a reimplementation of Chromaprint's CHROMAPRINT_ALGORITHM_TEST2, the
// algorithm fpcalc emits by default, from its MIT-licensed sources
// (https://github.com/acoustid/chromaprint). The pipeline is fixed and every
// constant is trained, so there is nothing here to tune:
//
//	audio → 11025 Hz mono → 4096-sample frames, hop 1365 → Hamming window
//	      → FFT power spectrum → 12 chroma bands → 5-tap smoothing over time
//	      → euclidean normalisation → integral image
//	      → 16 trained classifiers → 2 bits each, gray-coded → one uint32/frame
//
// # Agreeing with fpcalc
//
// The point is not to produce A fingerprint but one comparable with fpcalc's,
// because these are matched ACROSS machines: a phone's fingerprint of a track
// meets a server's. madshare compares them by bit error rate against a 10%
// threshold rather than for equality, which is the whole reason a reimplementation
// is viable — but it is a budget, not a licence. See the package tests, which
// measure against the real binary when it is installed.
//
// Two places where agreement is earned rather than assumed: the resampler
// (internal/resample, built to swresample's design and shared with playback)
// and the decoder, since a codec's own rounding is not ours to match. Both are
// measured, neither is asserted.
package chroma

import (
	"context"
	"errors"
	"math"

	"daemonlord.ygg/madplayer/internal/resample"
)

const (
	// targetRate is Chromaprint's sample rate, and the rate the resampler
	// converts every input to. Not a choice — the note table, the frame size
	// and every trained classifier threshold assume it.
	targetRate = 11025

	// frameSize and hop are Chromaprint's frame geometry: 4096 samples at
	// 11025 Hz (371 ms) advancing by a third of that.
	frameSize = 4096
	hop       = frameSize - (frameSize - frameSize/3)

	// minFreq and maxFreq bound the spectrum the chroma bands are built from.
	minFreq = 28
	maxFreq = 3520

	// silenceThreshold is the euclidean norm below which a chroma frame is
	// taken to be silence and zeroed rather than amplified into noise.
	silenceThreshold = 0.01
)

// chromaFilterCoefficients smooth each band over five consecutive frames.
var chromaFilterCoefficients = [5]float64{0.25, 0.75, 1.0, 0.75, 0.25}

// ErrTooShort is returned when the audio does not last long enough to produce a
// single sub-fingerprint. Chromaprint needs maxFilterWidth frames — about two
// and a half seconds — before the widest classifier has anything to read.
var ErrTooShort = errors.New("chroma: audio too short to fingerprint")

// Fingerprinter turns decoded audio into raw sub-fingerprints. Write as many
// blocks as the decoder produces, then call Finish.
//
// One fingerprint per instance: Finish drains buffered state and the zero value
// is not usable. Create one with New.
type Fingerprinter struct {
	resampler *resample.Resampler
	fft       *fft

	window   []float64
	notes    []int8 // spectrum bin → chroma band, -1 outside the band range
	frame    []float64
	spectrum []float64
	pending  []float64 // samples not yet forming a whole frame
	scratch  []float64 // resampler output, reused
	mono     []float64 // one block downmixed, reused

	features [numBands]float64
	history  [8][numBands]float64 // the 5-tap filter's ring, sized as Chromaprint sizes it
	offset   int
	filled   int

	image *integralImage
	print []uint32

	limit int64 // samples to accept, at targetRate
	taken int64
}

// New prepares a fingerprinter for audio arriving at the given sample rate.
//
// maxSeconds bounds how much audio is fingerprinted, and must match what the
// other side does: fpcalc reads the first 120 seconds unless told otherwise, so
// a fingerprint of a whole track would diverge from one of its first two
// minutes wherever the two are compared. Zero means no limit.
func New(sampleRate int, maxSeconds float64) (*Fingerprinter, error) {
	if sampleRate <= 0 {
		return nil, errors.New("chroma: sample rate must be positive")
	}
	f := &Fingerprinter{
		resampler: resample.New(sampleRate, targetRate),
		fft:       newFFT(frameSize),
		window:    hammingWindow(frameSize),
		frame:     make([]float64, frameSize),
		spectrum:  make([]float64, frameSize/2+1),
		pending:   make([]float64, 0, frameSize+hop),
		image:     newIntegralImage(256, numBands),
	}
	if maxSeconds > 0 {
		f.limit = int64(maxSeconds * targetRate)
	}
	f.prepareNotes()
	return f, nil
}

// prepareNotes assigns each spectrum bin to a pitch class, once. A bin's
// frequency maps to an octave, and the fractional part of the octave — where it
// sits between one C and the next — is its band.
func (f *Fingerprinter) prepareNotes() {
	f.notes = make([]int8, frameSize/2+1)
	for i := range f.notes {
		f.notes[i] = -1
	}
	minIndex := max(1, freqToIndex(minFreq))
	maxIndex := min(frameSize/2, freqToIndex(maxFreq))
	for i := minIndex; i < maxIndex; i++ {
		freq := float64(i) * targetRate / frameSize
		octave := math.Log2(freq / (440.0 / 16.0))
		f.notes[i] = int8(numBands * (octave - math.Floor(octave)))
	}
}

func freqToIndex(freq float64) int {
	return int(math.Round(frameSize * freq / targetRate))
}

// Done reports whether enough audio has been read. A caller decoding a long
// track can stop early instead of decoding minutes that will be discarded.
func (f *Fingerprinter) Done() bool {
	return f.limit > 0 && f.taken >= f.limit
}

// Write consumes a block of decoded samples as the player's decoders produce
// them: one pair per frame, left and right.
//
// The two channels are averaged, which is what ffmpeg's downmix does for
// fpcalc. A mono file arrives here with both channels equal, so the average is
// the sample itself.
func (f *Fingerprinter) Write(samples [][2]float64) {
	if f.Done() || len(samples) == 0 {
		return
	}
	// Reused, not allocated per block: a track is hundreds of blocks and this
	// runs while somebody waits for their library.
	if cap(f.mono) < len(samples) {
		f.mono = make([]float64, len(samples))
	}
	f.mono = f.mono[:len(samples)]
	for i, s := range samples {
		f.mono[i] = (s[0] + s[1]) / 2
	}
	f.scratch = f.resampler.Write(f.mono, f.scratch[:0])
	f.consume(f.scratch)
}

// consume takes resampled mono audio and turns whole frames into chroma rows.
func (f *Fingerprinter) consume(samples []float64) {
	if f.limit > 0 && f.taken+int64(len(samples)) > f.limit {
		samples = samples[:f.limit-f.taken]
	}
	f.taken += int64(len(samples))
	f.pending = append(f.pending, samples...)
	for len(f.pending) >= frameSize {
		f.transform(f.pending[:frameSize])
		f.pending = append(f.pending[:0], f.pending[hop:]...)
	}
}

// transform runs one frame through the spectrum and into the image.
func (f *Fingerprinter) transform(samples []float64) {
	// Quantise to int16 first. Chromaprint is fed int16 by fpcalc and its window
	// carries the 1/INT16_MAX scaling to match; feeding it un-rounded floats
	// would be a different input to the same trained thresholds.
	for i, s := range samples {
		f.frame[i] = float64(toInt16(s)) * f.window[i]
	}
	f.fft.power(f.frame, f.spectrum)

	f.features = [numBands]float64{}
	for i, band := range f.notes {
		if band >= 0 {
			f.features[band] += f.spectrum[i]
		}
	}
	f.smooth()
}

// toInt16 converts a decoder's float sample the way a decoder's int16 sample
// became one: scaled by 2^15 and clipped. Both of this program's paths — the
// decoders' own output and this — use the same scale, so audio that started as
// int16 arrives here unchanged.
func toInt16(s float64) int16 {
	v := math.Round(s * (1 << 15))
	if v > math.MaxInt16 {
		return math.MaxInt16
	}
	if v < math.MinInt16 {
		return math.MinInt16
	}
	return int16(v)
}

// smooth applies the five-tap filter across time, normalises, and hands the row
// to the image. The first four frames only fill the ring: Chromaprint emits
// nothing until the filter has a full window, and starting sooner would shift
// every sub-fingerprint against fpcalc's.
func (f *Fingerprinter) smooth() {
	f.history[f.offset] = f.features
	f.offset = (f.offset + 1) % len(f.history)
	if f.filled < len(chromaFilterCoefficients)-1 {
		f.filled++
		return
	}

	var row [numBands]float64
	start := (f.offset + len(f.history) - len(chromaFilterCoefficients)) % len(f.history)
	for j, c := range chromaFilterCoefficients {
		h := &f.history[(start+j)%len(f.history)]
		for i := range row {
			row[i] += h[i] * c
		}
	}

	// Euclidean normalisation is what makes the fingerprint indifferent to
	// volume. Below the threshold the frame is silence, and scaling silence up
	// to unit length would fingerprint the noise floor.
	var squares float64
	for _, v := range row {
		squares += v * v
	}
	if norm := math.Sqrt(squares); norm < silenceThreshold {
		row = [numBands]float64{}
	} else {
		for i := range row {
			row[i] /= norm
		}
	}

	f.image.addRow(row[:])
	if f.image.rows >= maxFilterWidth {
		f.print = append(f.print, f.subfingerprint(f.image.rows-maxFilterWidth))
	}
}

// subfingerprint packs the sixteen classifiers' verdicts about the frames
// starting at offset into one word, two gray-coded bits each.
func (f *Fingerprinter) subfingerprint(offset int) uint32 {
	var bits uint32
	for i := range classifiersTest2 {
		c := &classifiersTest2[i]
		bits = bits<<2 | grayCode[c.quantize(c.apply(f.image, offset))]
	}
	return bits
}

// Finish flushes what is buffered and returns the raw sub-fingerprint stream,
// in the same order and packing fpcalc's -raw output uses.
func (f *Fingerprinter) Finish() ([]uint32, error) {
	if !f.Done() {
		f.scratch = f.resampler.Flush(f.scratch[:0])
		f.consume(f.scratch)
	}
	if len(f.print) == 0 {
		return nil, ErrTooShort
	}
	return f.print, nil
}

// Compute is the whole thing over a decoder: pull blocks until the source is
// spent or the limit is reached.
//
// The stream function fills the buffer as beep's streamers do — it reports how
// many samples it wrote and whether the stream is still alive — so a caller can
// hand over a decoder without adapting it. ctx is checked between blocks, which
// is as fine-grained as it needs to be: one block is milliseconds.
func Compute(ctx context.Context, sampleRate int, maxSeconds float64, stream func([][2]float64) (int, bool)) ([]uint32, error) {
	f, err := New(sampleRate, maxSeconds)
	if err != nil {
		return nil, err
	}
	buf := make([][2]float64, 8192)
	for !f.Done() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		n, ok := stream(buf)
		if n > 0 {
			f.Write(buf[:n])
		}
		if !ok || n == 0 {
			break
		}
	}
	return f.Finish()
}
