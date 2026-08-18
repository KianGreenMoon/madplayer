package audio

import (
	"math"
	"sync"
	"testing"
	"time"

	"github.com/gopxl/beep/v2"
)

// counting streams a deterministic ramp so every frame's value encodes its
// index, which is what catches an interleave or offset mistake.
type counting struct{ n int }

func (c *counting) Stream(samples [][2]float64) (int, bool) {
	for i := range samples {
		samples[i][0] = float64(c.n) / 1e6
		samples[i][1] = -float64(c.n) / 1e6
		c.n++
	}
	return len(samples), true
}

func (c *counting) Err() error { return nil }

func decode(b []byte) float32 {
	bits := uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
	return math.Float32frombits(bits)
}

func TestStreamReaderEncodesInterleavedFloat32(t *testing.T) {
	var mu sync.Mutex
	r := &streamReader{mu: &mu, src: &counting{}}

	// 700 frames spans two of the reader's 512-frame chunks, so the seam is
	// covered; the extra 3 bytes must be left alone as a partial frame.
	buf := make([]byte, 700*frameBytes+3)
	buf[700*frameBytes] = 0xAA
	n, err := r.Read(buf)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if n != 700*frameBytes {
		t.Fatalf("Read returned %d bytes, want %d (whole frames only)", n, 700*frameBytes)
	}
	if buf[700*frameBytes] != 0xAA {
		t.Fatalf("Read wrote into the partial frame past %d", n)
	}
	for _, i := range []int{0, 1, 511, 512, 699} {
		wantL := float32(float64(i) / 1e6)
		if got := decode(buf[i*frameBytes:]); got != wantL {
			t.Fatalf("frame %d left = %v, want %v", i, got, wantL)
		}
		if got := decode(buf[i*frameBytes+4:]); got != -wantL {
			t.Fatalf("frame %d right = %v, want %v", i, got, -wantL)
		}
	}
}

func TestStreamReaderClampsOutOfRange(t *testing.T) {
	var mu sync.Mutex
	loud := beep.StreamerFunc(func(samples [][2]float64) (int, bool) {
		for i := range samples {
			samples[i][0] = 3.5
			samples[i][1] = -3.5
		}
		return len(samples), true
	})
	r := &streamReader{mu: &mu, src: loud}
	buf := make([]byte, 4*frameBytes)
	if _, err := r.Read(buf); err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got := decode(buf); got != 1 {
		t.Fatalf("hot left sample = %v, want clamped 1", got)
	}
	if got := decode(buf[4:]); got != -1 {
		t.Fatalf("hot right sample = %v, want clamped -1", got)
	}
}

// TestStreamReaderReportsStarvation pins the alarm: a pause between Reads
// longer than the pool plus the warn slack means oto mixed zeros, and that —
// and only that — calls late with the deficit. The clock is simulated by
// backdating lastRead; ordinary back-to-back Reads must stay quiet.
func TestStreamReaderReportsStarvation(t *testing.T) {
	var mu sync.Mutex
	var gaps []time.Duration
	r := &streamReader{
		mu:   &mu,
		src:  &counting{},
		rate: 44100,
		pool: 100 * time.Millisecond,
		warn: 25 * time.Millisecond,
		late: func(gap time.Duration) { gaps = append(gaps, gap) },
	}
	buf := make([]byte, 512*frameBytes)

	// Prime, then read again immediately: a full pool and no gap.
	for i := 0; i < 2; i++ {
		if _, err := r.Read(buf); err != nil {
			t.Fatalf("Read: %v", err)
		}
	}
	if len(gaps) != 0 {
		t.Fatalf("late called %d times on healthy reads", len(gaps))
	}

	// A second of silence on the wire: far past pool+warn, one report.
	r.lastRead = time.Now().Add(-time.Second)
	if _, err := r.Read(buf); err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(gaps) != 1 {
		t.Fatalf("late called %d times after a 1s stall, want 1", len(gaps))
	}
	if gaps[0] < 700*time.Millisecond || gaps[0] > time.Second {
		t.Fatalf("reported deficit %v, want roughly 1s minus the %v pool", gaps[0], r.pool)
	}

	// The report reset the model: the next quiet read stays quiet.
	if _, err := r.Read(buf); err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(gaps) != 1 {
		t.Fatalf("late called again (%d) without a new stall", len(gaps))
	}
}

// TestStreamReaderReportsSlowPulls pins the other half of the alarm: a source
// that takes longer than half the audio it delivers calls slow — once, not
// per read, because a decoder in trouble is in trouble on every read and the
// report must not flood the log it feeds. A fast source stays silent.
func TestStreamReaderReportsSlowPulls(t *testing.T) {
	var mu sync.Mutex
	var slow []time.Duration
	stats := &streamStats{}
	sluggish := beep.StreamerFunc(func(samples [][2]float64) (int, bool) {
		time.Sleep(12 * time.Millisecond)
		return len(samples), true
	})
	r := &streamReader{
		mu:    &mu,
		src:   sluggish,
		rate:  44100,
		slow:  func(exec, audio time.Duration) { slow = append(slow, exec) },
		stats: stats,
	}
	// 512 frames at 44.1 kHz is ~11.6ms of audio; one chunk sleeping 12ms is
	// past both tripwires (over 10ms, over half the audio's duration).
	buf := make([]byte, 512*frameBytes)
	for i := 0; i < 3; i++ {
		if _, err := r.Read(buf); err != nil {
			t.Fatalf("Read: %v", err)
		}
	}
	if len(slow) != 1 {
		t.Fatalf("slow called %d times in one second of trouble, want 1 (rate-limited)", len(slow))
	}
	if slow[0] < 12*time.Millisecond {
		t.Fatalf("reported exec %v, want at least the source's 12ms sleep", slow[0])
	}
	reads, worstExec, worstGap, starved, audio := stats.take()
	if reads != 3 {
		t.Fatalf("stats counted %d reads, want 3", reads)
	}
	if worstExec < 12*time.Millisecond {
		t.Fatalf("stats worst pull %v, want at least 12ms", worstExec)
	}
	if worstGap <= 0 {
		t.Fatalf("stats worst gap %v, want a positive wait between reads", worstGap)
	}
	if starved != 0 {
		t.Fatalf("stats counted %d starvations with no late callback wired", starved)
	}
	// Three reads of the same buffer, so the playing time delivered is three
	// times what one buffer holds. It is what the sink weighs the wall clock
	// against, and a wrong figure there would invent lag out of arithmetic.
	if want := 3 * r.rate.D(len(buf)/frameBytes); audio != want {
		t.Fatalf("stats counted %v of audio over three reads, want %v", audio, want)
	}
	if reads2, _, _, _, _ := stats.take(); reads2 != 0 {
		t.Fatalf("take did not reset: %d reads left behind", reads2)
	}

	// A source with no sleep must never trip it, however many reads pass.
	slow = nil
	r.src, r.slowAt = &counting{}, time.Time{}
	for i := 0; i < 5; i++ {
		if _, err := r.Read(buf); err != nil {
			t.Fatalf("Read: %v", err)
		}
	}
	if len(slow) != 0 {
		t.Fatalf("slow called %d times on a healthy source", len(slow))
	}
}

// TestStreamReaderMixerSilence pins the property the sink leans on: a mixer
// with nothing in it keeps streaming silence, so the device is fed zeros
// rather than starved (starvation is the bug this sink exists to fix).
func TestStreamReaderMixerSilence(t *testing.T) {
	var mu sync.Mutex
	var m beep.Mixer
	r := &streamReader{mu: &mu, src: &m}
	buf := make([]byte, 8*frameBytes)
	for i := range buf {
		buf[i] = 0xFF
	}
	n, err := r.Read(buf)
	if err != nil || n != len(buf) {
		t.Fatalf("Read = %d, %v; want %d, nil", n, err, len(buf))
	}
	for i := 0; i < 8; i++ {
		if decode(buf[i*frameBytes:]) != 0 || decode(buf[i*frameBytes+4:]) != 0 {
			t.Fatalf("empty mixer frame %d is not silence", i)
		}
	}
}
