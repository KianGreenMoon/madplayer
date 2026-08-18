package player

// The rate converter between a decoder and the device.
//
// beep ships one (beep.Resample) and this replaced it, for two measured
// reasons rather than a preference. beep interpolates with a Lagrange
// polynomial through quality*2 equally spaced samples — an interpolator with
// no anti-imaging filter in it, whose error grows towards Nyquist until it is
// as loud as the signal. Measured on 44100 → 48000 at the quality 8 this
// player was using, error below the tone: 74 dB at 10 kHz, 30 dB at 15 kHz,
// 11 dB at 19 kHz. The Kaiser-windowed sinc in internal/resample answers the
// same tones at 105 dB and 96 dB, and rolls off rather than distorting where
// the input band ends.
//
// The second reason is the phone. A 16-point Lagrange evaluation costs ~240
// multiplies and ~240 DIVIDES per sample per channel; 44100 → 48000 here is
// 34 multiply-adds against a table built once. Measured (BenchmarkKaiserSinc
// against BenchmarkBeepLagrange8, ten seconds of stereo off a slice): 27 ms
// against 175 ms, so **6× less work**, not the 100× the operation counts
// suggest — divides pipeline better than they look. The audio feed lands on
// a little core where the quality-8 resampler alone was measured eating
// 125–183 ms of every 250 ms of audio (2026-08-18) — the reason the resample
// had to be moved out of the audio pull in the first place. Six times less
// takes that to 20–30 ms, which is the difference between a fill that keeps
// up on a bad core and one that does not.
//
// It is NOT the fix for the crackle that hunt was chasing: the difference
// between the two converters on the album that crackles measures 55 dB below
// the music, which is not what a crackle sounds like. It is a cheaper, better
// converter, no more.

import (
	"github.com/gopxl/beep/v2"

	"daemonlord.ygg/madplayer/internal/resample"
)

// resampleChunk is how many source frames are pulled per refill. The same 512
// the fill and beep's mixer move, so nothing in the chain works in a size
// nothing else uses.
const resampleChunk = 512

// resampler streams src at the out rate.
//
// It differs from beep's in one behaviour besides the filter: a short read
// from src is not the end of the stream. beep's Resampler takes the first
// under-full read as end-of-data and never asks again, so a decoder that
// returns a partial chunk mid-track — which a decoder reading a growing file
// may — would end the track. Only ok=false ends it here.
type resampler struct {
	src      beep.Streamer
	l, r     *resample.Resampler
	in       [][2]float64 // one pull from src, reused
	mono     []float64    // one channel of it, reused
	outL     []float64    // resampled and not yet delivered
	outR     []float64
	at       int  // how much of outL/outR has been delivered
	drained  bool // src said ok=false
	finished bool // and its tail has been flushed
}

// newResampler converts src from in to out. The two rates must differ; equal
// rates need no wrapper and callers must not build one.
func newResampler(src beep.Streamer, in, out beep.SampleRate) *resampler {
	return &resampler{
		src:  src,
		l:    resample.New(int(in), int(out)),
		r:    resample.New(int(in), int(out)),
		in:   make([][2]float64, resampleChunk),
		mono: make([]float64, resampleChunk),
	}
}

func (rs *resampler) Stream(samples [][2]float64) (int, bool) {
	n := 0
	for n < len(samples) {
		if rs.at == len(rs.outL) {
			if !rs.refill() {
				return n, n > 0
			}
			continue
		}
		k := min(len(samples)-n, len(rs.outL)-rs.at)
		for i := 0; i < k; i++ {
			samples[n+i][0] = rs.outL[rs.at+i]
			samples[n+i][1] = rs.outR[rs.at+i]
		}
		rs.at += k
		n += k
	}
	return n, true
}

// refill pulls one chunk from src and resamples it, and reports whether
// anything came of it. False means the stream is over: src is drained and its
// tail flushed.
func (rs *resampler) refill() bool {
	rs.outL, rs.outR, rs.at = rs.outL[:0], rs.outR[:0], 0
	for len(rs.outL) == 0 {
		if rs.finished {
			return false
		}
		if rs.drained {
			// The half-kernel of input still inside the filter is real audio
			// and is owed; flushing pads it with the same silence the filter
			// assumed before the first sample.
			rs.outL = rs.l.Flush(rs.outL[:0])
			rs.outR = rs.r.Flush(rs.outR[:0])
			rs.finished = true
			break
		}
		got, ok := rs.src.Stream(rs.in)
		if !ok {
			rs.drained = true
		}
		if got > 0 {
			rs.mono = rs.mono[:got]
			for i := 0; i < got; i++ {
				rs.mono[i] = rs.in[i][0]
			}
			rs.outL = rs.l.Write(rs.mono, rs.outL[:0])
			for i := 0; i < got; i++ {
				rs.mono[i] = rs.in[i][1]
			}
			rs.outR = rs.r.Write(rs.mono, rs.outR[:0])
		}
	}
	return len(rs.outL) > 0
}

func (rs *resampler) Err() error { return rs.src.Err() }
