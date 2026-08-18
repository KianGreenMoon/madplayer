// LOCAL PATCH (madplayer): the tests behind PoolStats. See MADPLAYER-PATCH.md.
//
// They exist because the number they pin is one an embedder has to choose
// blind: how big a player's buffer must be for the device's reads never to
// come up short. This package answers a short buffer with SILENCE and no
// error, so the choice is unobservable from outside — hence PoolStats, and
// hence these, which measure what the choice actually buys.

package mux

import (
	"testing"
	"time"
)

const madplayerFrameBytes = 8 // stereo float32, madplayer's format

// gatedSource hands over a buffer full of 1.0 — but only when the test lets
// it, which is how a refill goroutine that has lost the CPU is modelled. The
// handshake is two-sided on purpose: a refill that lands at a moment the test
// did not choose moves the very level being measured.
type gatedSource struct {
	waiting chan struct{} // the mux loop has entered a read
	release chan struct{} // ... and may now finish it
}

func newGatedSource() *gatedSource {
	return &gatedSource{waiting: make(chan struct{}), release: make(chan struct{})}
}

func (g *gatedSource) Read(p []byte) (int, error) {
	g.waiting <- struct{}{}
	<-g.release
	for i := 0; i < len(p); i += 4 {
		p[i], p[i+1], p[i+2], p[i+3] = 0, 0, 0x80, 0x3f // 1.0, little-endian
	}
	return len(p), nil
}

// grant lets exactly one pending refill land and returns once it has.
func (g *gatedSource) grant(t *testing.T, p *Player) {
	t.Helper()
	select {
	case <-g.waiting:
	case <-time.After(5 * time.Second):
		t.Fatal("no refill was even attempted")
	}
	before := p.BufferedSize()
	g.release <- struct{}{}
	for i := 0; i < 5000; i++ {
		if p.BufferedSize() > before {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("the refill never landed: still %d bytes", before)
}

// topUp grants refills until the buffer is full — the state the mux loop
// leaves it in whenever nothing is wrong.
func (g *gatedSource) topUp(t *testing.T, p *Player, size int) {
	t.Helper()
	for i := 0; p.BufferedSize() < size; i++ {
		if i > 100 {
			t.Fatalf("the buffer never filled: %d of %d bytes", p.BufferedSize(), size)
		}
		g.grant(t, p)
	}
}

// device is one Stream::Loop read: it asks for frames and reports how many of
// them came back as silence.
func device(m *Mux, frames int) int {
	buf := make([]float32, frames*2)
	m.ReadFloat32s(buf)
	silent := 0
	for i := 0; i < len(buf); i += 2 {
		if buf[i] == 0 {
			silent++
		}
	}
	return silent
}

func TestAShortBufferIsMixedAsSilenceAndCounted(t *testing.T) {
	const rate = 48000
	g := newGatedSource()
	m := New(rate, 2, FormatFloat32LE)
	p := m.NewPlayer(g)
	size := rate / 10 * madplayerFrameBytes // 100 ms
	p.SetBufferSize(size)
	go p.Play()
	g.topUp(t, p, size)

	// Ask for 250 ms with 100 ms buffered and no refill coming: 150 ms of the
	// answer is silence nobody is told about.
	want := rate/4 - rate/10
	if silent := device(m, rate/4); silent != want {
		t.Fatalf("silent frames = %d, want %d", silent, want)
	}
	st := p.TakePoolStats()
	if st.Shorts != 1 || st.ShortBytes != want*madplayerFrameBytes {
		t.Fatalf("PoolStats = %+v, want 1 short of %d bytes", st, want*madplayerFrameBytes)
	}
	if st.LowWater != 0 {
		t.Fatalf("LowWater = %d, want 0 — the buffer was emptied", st.LowWater)
	}
	if st.MaxRead != rate/4*madplayerFrameBytes {
		t.Fatalf("MaxRead = %d, want %d", st.MaxRead, rate/4*madplayerFrameBytes)
	}
	if again := p.TakePoolStats(); again.Reads != 0 {
		t.Fatalf("the window did not reset: %+v", again)
	}
}

// stallTolerance is how long the device can keep reading, in ms of audio,
// before this player's buffer comes up short with no refill landing. phase
// picks where in the buffer's sawtooth the stall begins.
func stallTolerance(t *testing.T, rate, poolMS, readMS, phase int) int {
	t.Helper()
	g := newGatedSource()
	m := New(rate, 2, FormatFloat32LE)
	p := m.NewPlayer(g)
	size := rate * poolMS / 1000 * madplayerFrameBytes
	p.SetBufferSize(size)
	go p.Play()
	g.topUp(t, p, size)

	for i := 0; i < phase; i++ {
		if silent := device(m, rate*readMS/1000); silent != 0 {
			t.Fatalf("phase walk hit silence at %d", i)
		}
		g.topUp(t, p, size)
	}
	for i := 0; ; i++ { // from here no refill is granted
		if silent := device(m, rate*readMS/1000); silent > 0 {
			return i * readMS
		}
		if i > 200 {
			t.Fatal("no silence in 200 reads")
		}
	}
}

// TestThePoolIsTheOnlyThingThatBuysTimeForALateRefill pins the two facts an
// embedder's buffer size has to be chosen from, and one that looks like it
// should matter and does not.
//
// A player is refilled when its buffer falls BELOW bufferSize and is then
// topped up by a whole one, so the level sawtooths between B and 2B and the
// worst phase to stall in leaves B-X, where X is the device's read. Hence:
//
//   - the buffer buys time in proportion to its size: 250 ms survives 240 ms
//     of stalled refill, 500 ms survives 480 ms;
//   - the size of the device's read does NOT change that time — only how big
//     the resulting hole is when the time runs out.
func TestThePoolIsTheOnlyThingThatBuysTimeForALateRefill(t *testing.T) {
	const rate = 48000
	for _, c := range []struct {
		poolMS, readMS, wantWorst int
	}{
		{250, 120, 240}, // madplayer on a Pixel 7 Pro: 250 ms pool, oto reads 3×40 ms
		{250, 40, 240},  // a third of the read size, the same tolerance
		{500, 120, 480}, // twice the pool, twice the tolerance
		{500, 40, 480},
	} {
		worst := 1 << 30
		// The level drifts by (pool mod read) per cycle, so the sweep has to
		// be longer than one sawtooth to be sure the worst phase is in it.
		for phase := 0; phase < 2*c.poolMS/c.readMS+4; phase++ {
			if got := stallTolerance(t, rate, c.poolMS, c.readMS, phase); got < worst {
				worst = got
			}
		}
		if worst != c.wantWorst {
			t.Errorf("pool %dms, device read %dms: survived %dms of stalled refill, want %dms",
				c.poolMS, c.readMS, worst, c.wantWorst)
		}
	}
}
