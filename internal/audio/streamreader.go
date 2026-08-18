package audio

import (
	"math"
	"sync"
	"sync/atomic"
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

	// slow and stats split the starvation alarm into its two possible causes.
	// late above says zeros were mixed; it cannot say WHY. slow fires when
	// one Read spends longer decoding than half the audio it produced — the
	// chain below (resampler, decoder, a lock held by the UI) is what is too
	// slow. A starved pool WITHOUT slow pulls means Read was not called: the
	// scheduler, a GC pause, or the platform freezing the process. stats
	// counts every read for the periodic line the sink logs, so the two can
	// be told apart after the fact from a phone's Debugging page.
	slow  func(exec, audio time.Duration)
	stats *streamStats

	level    time.Duration
	lastRead time.Time
	slowAt   time.Time
}

// streamStats is the between-logs accumulator behind the sink's periodic
// stats line. One goroutine writes (oto's reader), one takes and resets (the
// sink's ticker); atomics rather than a lock because the writer is the audio
// path, which must never wait on a logger.
type streamStats struct {
	reads     atomic.Int64
	worstExec atomic.Int64 // ns, longest single Read
	worstGap  atomic.Int64 // ns, longest wait between Reads
	starved   atomic.Int64 // late-callback firings
	// audio is how much PLAYING TIME has been handed over since the last
	// take. Against the wall clock it is the one measurement in this program
	// that can see a glitch nobody here caused.
	//
	// Everything else watches this process feeding the device. Below the
	// device's Go-side pool sit oto's C++ fifo, oboe's OpenSL stream,
	// AudioFlinger's mixer, the HAL — and when any of those runs dry, what
	// reaches the ear is silence THIS PROGRAM never produced and no counter
	// here records. But the samples are not thrown away: they play late. So
	// the audio handed over falls behind the wall clock by exactly the
	// silence that was inserted, and it keeps falling behind, one gap at a
	// time. Thirty seconds of reads carrying 29.8 s of audio is 200 ms of
	// something below us — invisible everywhere else, including in a
	// worst-gap figure, because the fifo absorbs the lateness that caused it.
	//
	// The noise floor is the two clocks disagreeing: a device crystal is
	// good to about 100 ppm, which is 3 ms per 30 s and drifts steadily in
	// one direction. Dropouts do not drift, they JUMP — so read the trend,
	// and treat anything under a few tens of milliseconds as the crystal.
	audio atomic.Int64 // ns of playing time delivered

	// restarted is the sink saying it threw pooled audio away — a track
	// change, a seek, a stop. That audio was delivered and never played, so
	// it would read as lag; the accounting starts over instead.
	restarted atomic.Bool
}

func (s *streamStats) take() (reads int64, worstExec, worstGap time.Duration, starved int64, audio time.Duration) {
	return s.reads.Swap(0),
		time.Duration(s.worstExec.Swap(0)),
		time.Duration(s.worstGap.Swap(0)),
		s.starved.Swap(0),
		time.Duration(s.audio.Swap(0))
}

func peak(a *atomic.Int64, v int64) {
	if v > a.Load() {
		a.Store(v) // single writer, so load-then-store cannot lose a bigger value
	}
}

func (r *streamReader) Read(p []byte) (int, error) {
	nFrames := len(p) / frameBytes
	now := time.Now()
	var gap time.Duration
	if !r.lastRead.IsZero() {
		gap = now.Sub(r.lastRead)
	}
	if r.late != nil && r.rate > 0 {
		r.level -= gap
		if r.level < -r.warn {
			r.late(-r.level)
			if r.stats != nil {
				r.stats.starved.Add(1)
			}
			r.level = 0 // one dry spell, one report
		}
		r.lastRead = now
		r.level += r.rate.D(nFrames)
		if r.level > r.pool {
			r.level = r.pool
		}
	} else {
		r.lastRead = now
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
	exec := time.Since(now)
	if r.stats != nil {
		r.stats.reads.Add(1)
		peak(&r.stats.worstExec, int64(exec))
		peak(&r.stats.worstGap, int64(gap))
		r.stats.audio.Add(int64(r.rate.D(nFrames)))
	}
	if r.slow != nil && r.rate > 0 {
		audio := r.rate.D(nFrames)
		// Half of realtime is the tripwire: a pull that slow leaves no margin,
		// and a train of them is the pool draining. At most one report a
		// second — a decoder in real trouble would otherwise write a line per
		// read, and flooding the log it is meant to diagnose helps nobody.
		if exec > audio/2 && exec > 10*time.Millisecond && now.Sub(r.slowAt) > time.Second {
			r.slowAt = now
			r.slow(exec, audio)
		}
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
