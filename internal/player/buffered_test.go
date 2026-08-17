package player

// The decode-ahead ring. The property under test is the one the Android ANR
// of 2026-08-17 was missing: nothing the UI can end up waiting behind may
// ever wait on the network. The ring's Stream must answer immediately —
// silence when the download is behind — and the playhead must count only the
// audio that really played.

import (
	"sync/atomic"
	"testing"
	"time"

	"daemonlord.ygg/madplayer/internal/queue"
)

// scripted is a decoder stand-in the test feeds by hand. Its Stream BLOCKS
// until samples are fed or the channel is closed — which is exactly what a
// decoder over a stalled download does, and the behaviour the ring exists to
// keep away from every lock.
type scripted struct {
	ch     chan [2]float64
	primed int
	closes atomic.Int32
}

func newScripted(primed int) *scripted {
	return &scripted{ch: make(chan [2]float64), primed: primed}
}

func (s *scripted) Stream(out [][2]float64) (int, bool) {
	// Block for the first sample — that is the stall being simulated — then
	// take whatever else is immediately there. A real decoder likewise
	// returns the samples it has rather than waiting out a full buffer.
	v, ok := <-s.ch
	if !ok {
		return 0, false
	}
	out[0] = v
	i := 1
	for i < len(out) {
		select {
		case v, ok := <-s.ch:
			if !ok {
				return i, true // drained; the next call reports the end
			}
			out[i] = v
			i++
		default:
			return i, true
		}
	}
	return i, true
}

func (s *scripted) Err() error    { return nil }
func (s *scripted) Len() int      { return 0 }
func (s *scripted) Position() int { return s.primed }
func (s *scripted) Seek(int) error {
	return nil
}
func (s *scripted) Close() error {
	s.closes.Add(1)
	return nil
}

// feed pushes n recognisable samples, so a test can tell delivered audio from
// the ring's padding.
func (s *scripted) feed(n int) {
	for i := 1; i <= n; i++ {
		s.ch <- [2]float64{float64(i), float64(i)}
	}
}

// A dry ring answers with silence at once — never a wait, never a short
// return. A short return would END the track (beep's Mixer drops a streamer
// that under-fills, its Resampler reads a short fill as end-of-data), and a
// wait is the freeze: Stream runs under the sink lock the UI takes for the
// playhead on every frame.
func TestADryRingPadsWithSilenceImmediately(t *testing.T) {
	inner := newScripted(0)
	b := newBuffered(inner, SampleRate)
	defer func() { close(inner.ch); <-b.exited }()

	buf := make([][2]float64, 64)
	buf[0] = [2]float64{9, 9} // stale data the pad must overwrite

	type result struct {
		n  int
		ok bool
	}
	got := make(chan result, 1)
	go func() {
		n, ok := b.Stream(buf)
		got <- result{n, ok}
	}()
	select {
	case r := <-got:
		if r.n != len(buf) || !r.ok {
			t.Fatalf("Stream = (%d, %v), want a full silent buffer (%d, true)", r.n, r.ok, len(buf))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Stream blocked on a stalled decoder — the wedge the ring exists to remove")
	}
	for i, v := range buf {
		if v != ([2]float64{}) {
			t.Fatalf("sample %d = %v, want silence", i, v)
		}
	}
	if got := b.Position(); got != 0 {
		t.Errorf("Position = %d after pure padding, want 0 — silence is not audio that played", got)
	}
}

// Real samples come through in order and are the only thing the playhead
// counts; the decoder finishing ends the stream after the last of them.
func TestTheRingDeliversAndCountsOnlyRealAudio(t *testing.T) {
	inner := newScripted(0)
	b := newBuffered(inner, SampleRate)

	const fed = 100
	go func() {
		inner.feed(fed)
		close(inner.ch)
	}()
	waitFor(t, "the fill to drain the decoder", func() bool {
		b.mu.Lock()
		defer b.mu.Unlock()
		return b.done
	})
	<-b.exited // the decoder is closed by the fill on its way out

	buf := make([][2]float64, 64)
	n, ok := b.Stream(buf)
	if n != 64 || !ok {
		t.Fatalf("first Stream = (%d, %v), want (64, true)", n, ok)
	}
	n2, ok2 := b.Stream(buf[:64])
	if n2 != fed-64 || ok2 {
		t.Fatalf("final Stream = (%d, %v), want (%d, false) — the last samples end the track", n2, ok2, fed-64)
	}
	if got := b.Position(); got != fed {
		t.Errorf("Position = %d, want %d", got, fed)
	}
	if got := inner.closes.Load(); got != 1 {
		t.Errorf("the decoder was closed %d times by the fill, want 1", got)
	}
}

// The playhead starts where the decoder stood at wrap time. rangeseek
// calibrates base against exactly that value, so a ring that reset it to zero
// would shear every mid-stream seek by the primed amount.
func TestTheRingStartsAtTheDecodersPosition(t *testing.T) {
	inner := newScripted(4200)
	b := newBuffered(inner, SampleRate)
	defer func() { close(inner.ch); <-b.exited }()

	if got := b.Position(); got != 4200 {
		t.Fatalf("Position at wrap = %d, want the decoder's 4200", got)
	}
	go inner.feed(10)
	waitFor(t, "the fill to buffer the samples", func() bool {
		b.mu.Lock()
		defer b.mu.Unlock()
		return b.count >= 10
	})
	buf := make([][2]float64, 10)
	b.Stream(buf)
	if got := b.Position(); got != 4210 {
		t.Errorf("Position after 10 samples = %d, want 4210", got)
	}
}

// Close returns without waiting for the fill: it is called under the
// player's lock, and a fill parked inside a blocked read only wakes when the
// load context is cancelled — waiting here would re-create the wedge. The
// fill still closes the decoder on its way out.
func TestCloseDoesNotWaitForAParkedFill(t *testing.T) {
	inner := newScripted(0)
	b := newBuffered(inner, SampleRate)
	// The fill is (or soon will be) parked inside inner.Stream with nothing
	// fed — the shape of a download that has gone quiet.

	done := make(chan struct{})
	go func() {
		if err := b.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Close waited for a fill parked on the network")
	}

	if n, ok := b.Stream(make([][2]float64, 8)); n != 0 || ok {
		t.Errorf("Stream after Close = (%d, %v), want (0, false)", n, ok)
	}

	// The caller's cancellation is what wakes the parked read in real use;
	// here the stand-in's end does the same job.
	close(inner.ch)
	select {
	case <-b.exited:
	case <-time.After(2 * time.Second):
		t.Fatal("the fill never exited after its read returned")
	}
	if got := inner.closes.Load(); got != 1 {
		t.Errorf("the decoder was closed %d times, want 1", got)
	}
}

// A dry spell is one gap, not a crackle. Once the ring has run dry, a trickle
// of samples must NOT be delivered as it arrives — audio and silence
// alternating at buffer rate is the intermittent crackle heard on remote
// tracks (2026-08-17) — so the stream stays silent until rearm's worth is
// buffered again, then resumes faded in.
func TestADrySpellIsOneGapNotACrackle(t *testing.T) {
	inner := newScripted(0)
	// A tiny rate keeps the numbers readable: ring 800, rearm 50.
	b := newBuffered(inner, 100)
	defer func() { close(inner.ch); <-b.exited }()
	if b.rearm != 50 {
		t.Fatalf("rearm = %d, want 50 at rate 100", b.rearm)
	}

	go inner.feed(20)
	waitFor(t, "the fill to buffer the opening samples", func() bool {
		b.mu.Lock()
		defer b.mu.Unlock()
		return b.count >= 20
	})
	buf := make([][2]float64, 20)
	if n, ok := b.Stream(buf); n != 20 || !ok {
		t.Fatalf("opening Stream = (%d, %v), want (20, true)", n, ok)
	}

	// Dry: this pad opens the spell.
	if n, ok := b.Stream(buf[:8]); n != 8 || !ok {
		t.Fatalf("padding Stream = (%d, %v), want (8, true)", n, ok)
	}

	// A trickle below rearm arrives. Delivering it now would be the crackle.
	go inner.feed(30)
	waitFor(t, "the trickle to be buffered", func() bool {
		b.mu.Lock()
		defer b.mu.Unlock()
		return b.count >= 30
	})
	if n, ok := b.Stream(buf[:8]); n != 8 || !ok {
		t.Fatalf("gated Stream = (%d, %v), want (8, true)", n, ok)
	}
	for i := 0; i < 8; i++ {
		if buf[i] != ([2]float64{}) {
			t.Fatalf("sample %d = %v while gated, want silence — a delivered trickle is the crackle", i, buf[i])
		}
	}
	if got := b.Position(); got != 20 {
		t.Errorf("Position = %d while gated, want 20 — gated silence is not audio that played", got)
	}

	// The ring reaches rearm; audio resumes, faded in.
	go inner.feed(30)
	waitFor(t, "the ring to rearm", func() bool {
		b.mu.Lock()
		defer b.mu.Unlock()
		return b.count >= b.rearm
	})
	if n, ok := b.Stream(buf[:8]); n != 8 || !ok {
		t.Fatalf("resumed Stream = (%d, %v), want (8, true)", n, ok)
	}
	for i := 0; i < 8; i++ {
		if buf[i] == ([2]float64{}) {
			t.Fatalf("sample %d silent after the ring rearmed, want audio", i)
		}
	}
	if buf[0][0] >= 1 {
		t.Errorf("first resumed sample = %v, want it faded in below the fed value 1", buf[0])
	}
	if got := b.Position(); got != 28 {
		t.Errorf("Position = %d after resuming, want 28", got)
	}
}

// The edge where audio meets padding is ramped: a hard cut to silence is the
// click half of the crackle. The last real sample before a pad must reach
// exactly zero, so the silence continues it seamlessly.
func TestPaddingEdgesAreRamped(t *testing.T) {
	inner := newScripted(0)
	b := newBuffered(inner, 100)
	defer func() { close(inner.ch); <-b.exited }()

	go inner.feed(4)
	waitFor(t, "the fill to buffer the samples", func() bool {
		b.mu.Lock()
		defer b.mu.Unlock()
		return b.count >= 4
	})
	buf := make([][2]float64, 8)
	if n, ok := b.Stream(buf); n != 8 || !ok {
		t.Fatalf("Stream = (%d, %v), want a full padded buffer (8, true)", n, ok)
	}
	// Four real samples (values 1..4), faded out over their whole length:
	// gains 3/4, 2/4, 1/4, 0 — all exact in binary.
	want := [][2]float64{{0.75, 0.75}, {1, 1}, {0.75, 0.75}, {0, 0}}
	for i, w := range want {
		if buf[i] != w {
			t.Errorf("sample %d = %v, want %v (fed value %d faded out)", i, buf[i], w, i+1)
		}
	}
	for i := 4; i < 8; i++ {
		if buf[i] != ([2]float64{}) {
			t.Errorf("pad sample %d = %v, want silence", i, buf[i])
		}
	}
}

// The whole chain, as the phone runs it: a streaming track stalls mid-song
// while the device keeps pulling, and the calls the UI makes every frame
// still answer. Before the ring, the pull blocked inside the decoder holding
// the sink lock, Position() queued behind it holding the player's lock, and
// the window froze for as long as the download stalled — the ANR this test
// pins. It also checks the other half of the bargain: when the bytes resume,
// so does the audio.
func TestAStalledStreamLeavesThePlayerResponsive(t *testing.T) {
	dir := t.TempDir()
	path := writeWAV(t, dir, "a.wav", 4)

	// A tenth of a second downloads, then the link goes quiet.
	fetch := &gatedFetcher{path: path, limit: 44 + 8820, open: make(chan struct{})}
	sink := &fakeSink{}
	p, err := New(sink)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	p.SetFetcher(fetch)

	p.SetQueue([]*queue.Item{{URL: "https://elsewhere/a.wav", Duration: 4}}, 0)
	waitPlaying(t, p)

	// The device pulls throughout, like the phone's does.
	stopPump := make(chan struct{})
	pumpDone := make(chan struct{})
	go func() {
		defer close(pumpDone)
		for {
			select {
			case <-stopPump:
				return
			default:
			}
			sink.pump(64)
			time.Sleep(time.Millisecond)
		}
	}()

	// Everything the UI asks per frame, during the stall.
	answered := make(chan struct{})
	go func() {
		for i := 0; i < 50; i++ {
			p.Position()
			p.Playing()
			p.Loading()
			p.Seekable()
		}
		close(answered)
	}()
	select {
	case <-answered:
	case <-time.After(2 * time.Second):
		t.Fatal("the UI's calls blocked behind a stalled download")
	}

	close(fetch.open) // the link comes back
	waitFor(t, "playback to move again once the bytes arrived", func() bool {
		elapsed, _ := p.Position()
		return elapsed > 0.2
	})
	close(stopPump)
	<-pumpDone
}
