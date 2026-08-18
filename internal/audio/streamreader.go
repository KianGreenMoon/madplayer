package audio

import (
	"math"
	"sync"
	"time"

	"github.com/gopxl/beep/v2"
)

// frameBytes is one interleaved stereo frame of float32 samples — the format
// the Android sink asks oto for. It is defined here, off the android build
// tag, so the encoder and its tests run on every platform.
const frameBytes = 8

// streamReader adapts a beep.Streamer to the io.Reader an oto player pulls
// from, encoding interleaved little-endian float32 stereo. It is the
// counterpart of beep/speaker's own reader, which is unexported and hardwired
// to int16.
//
// mu is the sink's lock: the player mutates running streamers under it, so
// every Stream call happens inside it. It is taken per chunk rather than per
// Read — a Read can cover hundreds of milliseconds of audio, and a UI thread
// asking for the playhead should not wait out a whole decode.
//
// rate/pool/warn/late are the starvation alarm. oto's mixer never blocks on
// this reader: when the pool it fills runs dry, the device mix is padded with
// zeros — an audible gap that no downstream counter records, because the
// device stays fed (with silence). The only observable is right here, so the
// reader models the pool: each Read tops it up by the audio it delivers
// (capped at pool), and the wall clock drains it. A deficit deeper than warn
// — slack for the driver reading in bursts — means zeros were mixed, and
// late is called with roughly how much. A sink that wants the evidence sets
// all four; left zero, Read is exactly what it always was.
type streamReader struct {
	mu   *sync.Mutex
	src  beep.Streamer
	rate beep.SampleRate
	pool time.Duration
	warn time.Duration
	late func(gap time.Duration)

	level    time.Duration
	lastRead time.Time
}

func (r *streamReader) Read(p []byte) (int, error) {
	nFrames := len(p) / frameBytes
	if r.late != nil && r.rate > 0 {
		now := time.Now()
		if !r.lastRead.IsZero() {
			r.level -= now.Sub(r.lastRead)
			if r.level < -r.warn {
				r.late(-r.level)
				r.level = 0 // one dry spell, one report
			}
		}
		r.lastRead = now
		r.level += r.rate.D(nFrames)
		if r.level > r.pool {
			r.level = r.pool
		}
	}
	var tmp [512][2]float64
	for done := 0; done < nFrames; {
		n := min(len(tmp), nFrames-done)
		r.mu.Lock()
		r.src.Stream(tmp[:n])
		r.mu.Unlock()
		for i := 0; i < n; i++ {
			off := (done + i) * frameBytes
			putSample(p[off:], tmp[i][0])
			putSample(p[off+4:], tmp[i][1])
		}
		done += n
	}
	return nFrames * frameBytes, nil
}

// putSample writes one sample as little-endian float32, clamped to [-1, 1]:
// the device takes floats as-is, and a hot mix that leaves the range would
// wrap into full-scale noise rather than the mild clipping the ear forgives.
func putSample(b []byte, v float64) {
	if v > 1 {
		v = 1
	} else if v < -1 {
		v = -1
	}
	bits := math.Float32bits(float32(v))
	b[0] = byte(bits)
	b[1] = byte(bits >> 8)
	b[2] = byte(bits >> 16)
	b[3] = byte(bits >> 24)
}
