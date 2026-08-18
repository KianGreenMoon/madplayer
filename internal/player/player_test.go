package player

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gopxl/beep/v2"
	"github.com/gopxl/beep/v2/wav"

	"daemonlord.ygg/madplayer/internal/queue"
)

// fakeSink stands in for the audio device. Everything the player decides —
// advancing, repeat, error recovery, seeking — is testable through it on a
// machine with no sound card, which is the reason Sink is an interface.
type fakeSink struct {
	mu     sync.Mutex
	s      beep.Streamer
	rate   beep.SampleRate
	paused bool // the device-level hold, which is not the mixer's Ctrl
}

// rate, when set, is what the fake device claims to run at — the way a phone
// answers 48000 to a 44100 request.
func (f *fakeSink) Init(rate beep.SampleRate, _ int) (beep.SampleRate, error) {
	if f.rate != 0 {
		return f.rate, nil
	}
	return rate, nil
}
func (f *fakeSink) Lock()                           { f.mu.Lock() }
func (f *fakeSink) Unlock()                         { f.mu.Unlock() }
func (f *fakeSink) Close() error                    { return nil }

func (f *fakeSink) Play(s beep.Streamer) {
	f.mu.Lock()
	f.s = s
	f.mu.Unlock()
}

func (f *fakeSink) Clear() {
	f.mu.Lock()
	f.s = nil
	f.mu.Unlock()
}

func (f *fakeSink) SetPaused(paused bool) {
	f.mu.Lock()
	f.paused = paused
	f.mu.Unlock()
}

func (f *fakeSink) held() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.paused
}

// pump pulls samples the way a real device would, until the stream ends or the
// budget runs out.
func (f *fakeSink) pump(maxBlocks int) {
	buf := make([][2]float64, 512)
	for i := 0; i < maxBlocks; i++ {
		f.mu.Lock()
		s := f.s
		if s == nil {
			f.mu.Unlock()
			return
		}
		n, ok := s.Stream(buf)
		f.mu.Unlock()
		if !ok || n == 0 {
			return
		}
	}
}

// writeWAV synthesises a decodable file of the given length. Generating one is
// what keeps these tests hermetic — no fixture audio in the repo, no licence
// question, and the exact duration is known.
func writeWAV(t *testing.T, dir, name string, seconds float64) string {
	t.Helper()
	path := filepath.Join(dir, name)
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	format := beep.Format{SampleRate: 44100, NumChannels: 1, Precision: 2}
	if err := wav.Encode(f, beep.Silence(int(seconds*44100)), format); err != nil {
		t.Fatal(err)
	}
	return path
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// waitPlaying waits for a track to be open and running.
//
// Starting a track is asynchronous — a remote one has to be downloaded first,
// and resolving inline would freeze the window for the length of that download
// — so a test that acts the instant SetQueue returns is acting before there is
// anything to act on.
func waitPlaying(t *testing.T, p *Player) {
	t.Helper()
	waitFor(t, "the track to start", p.Playing)
}

func TestProbeReportsDuration(t *testing.T) {
	dir := t.TempDir()
	path := writeWAV(t, dir, "a.wav", 2.5)

	got, err := Probe(path)
	if err != nil {
		t.Fatal(err)
	}
	if got < 2.4 || got > 2.6 {
		t.Errorf("duration = %.3f, want ~2.5", got)
	}
}

func TestUnsupportedFormatIsItsOwnError(t *testing.T) {
	// The scanner indexes these because the server accepts them; this build has
	// no decoder. The UI needs to tell that apart from a missing file, so it is
	// a distinct error type rather than a generic failure.
	if Decodable("/m/x.m4a") {
		t.Error("m4a reported decodable; no pure-Go decoder is wired up")
	}
	if !Decodable("/m/x.FLAC") {
		t.Error("extension match should be case-insensitive")
	}

	_, err := Probe("/m/nope.m4a")
	var unsup *ErrUnsupported
	if !errors.As(err, &unsup) {
		t.Errorf("err = %v, want ErrUnsupported", err)
	}
}

// TestPlaybackRunsAtTheDeviceRate pins the native-rate contract: when the
// device answers a different rate than requested, playback is resampled to
// the ANSWER — a 44.1 kHz track on an 88.2 kHz device must deliver twice its
// samples. Before this contract the player resampled everything to its own
// constant and the device's driver silently resampled AGAIN, with a mid-
// quality converter nobody chose (heard on the phone as worse-than-browser
// sound).
func TestPlaybackRunsAtTheDeviceRate(t *testing.T) {
	dir := t.TempDir()
	path := writeWAV(t, dir, "a.wav", 0.5) // 22050 samples at 44.1 kHz

	sink := &fakeSink{rate: 88200}
	p, err := New(sink)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	p.SetQueue([]*queue.Item{{Path: path}}, 0)
	waitPlaying(t, p)

	// Let the fill finish the track first: the resampler runs in the ring's
	// fill now, and this loop pumps orders of magnitude faster than any real
	// device — unpaced, it would burn its budget on the ring's padding before
	// the fill has converted a single chunk.
	b := p.src.buf
	waitFor(t, "the fill to convert the whole track", func() bool {
		b.mu.Lock()
		defer b.mu.Unlock()
		return b.done
	})

	got := 0
	buf := make([][2]float64, 512)
	for i := 0; i < 1000; i++ {
		sink.mu.Lock()
		s := sink.s
		if s == nil {
			sink.mu.Unlock()
			break
		}
		n, ok := s.Stream(buf)
		sink.mu.Unlock()
		got += n
		if !ok {
			break
		}
	}
	// Half a second at the device's 88.2 kHz. The resampler's edges wobble by
	// a chunk, so the bound is loose — what matters is 2×, not 1×.
	want := 44100
	if got < want-1024 || got > want+1024 {
		t.Fatalf("track delivered %d samples at the device rate, want ~%d (2x its own)", got, want)
	}
}

func TestTrackEndAdvancesTheQueue(t *testing.T) {
	dir := t.TempDir()
	a := writeWAV(t, dir, "a.wav", 0.05)
	b := writeWAV(t, dir, "b.wav", 0.05)

	sink := &fakeSink{}
	p, err := New(sink)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	p.SetQueue([]*queue.Item{{Path: a}, {Path: b}}, 0)
	waitPlaying(t, p)
	sink.pump(1000)

	waitFor(t, "the queue to advance to the second track", func() bool {
		return p.QueueIndex() == 1
	})
	if got := p.Current().Path; got != b {
		t.Errorf("current = %s, want %s", got, b)
	}
}

func TestRepeatOffStopsAtTheEnd(t *testing.T) {
	dir := t.TempDir()
	a := writeWAV(t, dir, "a.wav", 0.05)

	sink := &fakeSink{}
	p, _ := New(sink)
	defer p.Close()

	p.SetQueue([]*queue.Item{{Path: a}}, 0)
	waitPlaying(t, p)
	sink.pump(1000)

	waitFor(t, "playback to stop at the end of the queue", func() bool {
		return !p.Playing()
	})
}

func TestAnUnplayableTrackAdvancesRatherThanStalling(t *testing.T) {
	dir := t.TempDir()
	good := writeWAV(t, dir, "good.wav", 0.05)
	bad := filepath.Join(dir, "bad.wav")
	if err := os.WriteFile(bad, []byte("this is not audio"), 0o644); err != nil {
		t.Fatal(err)
	}

	sink := &fakeSink{}
	p, _ := New(sink)
	defer p.Close()

	// Start on the broken file: the queue must move past it, not stop dead.
	p.SetQueue([]*queue.Item{{Path: bad}, {Path: good}}, 0)

	waitFor(t, "the queue to skip the unplayable track", func() bool {
		return p.QueueIndex() == 1
	})

	// The record must survive the NEXT track playing fine — otherwise the very
	// skip the failure caused is what erases the evidence of it.
	if p.Unplayable(queue.Key(bad, "")) == nil {
		t.Error("the broken track is not marked unplayable; its row cannot be flagged")
	}
	if p.Unplayable(queue.Key(good, "")) != nil {
		t.Error("the good track was marked unplayable")
	}
	if p.TakeError() == nil {
		t.Error("no error to report to the user")
	}
	if p.TakeError() != nil {
		t.Error("TakeError should consume: a reported failure must not re-announce itself")
	}
}

func TestSeekStaysInsideTheTrack(t *testing.T) {
	dir := t.TempDir()
	a := writeWAV(t, dir, "a.wav", 3)

	sink := &fakeSink{}
	p, _ := New(sink)
	defer p.Close()

	p.SetQueue([]*queue.Item{{Path: a}}, 0)
	waitPlaying(t, p)

	// Scrubbing to the very end must not read as "the track ended" and skip on.
	p.Seek(99)
	elapsed, total := p.Position()
	if total < 2.9 || total > 3.1 {
		t.Errorf("total = %.2f, want ~3", total)
	}
	if elapsed >= total {
		t.Errorf("elapsed %.3f reached total %.3f — a scrub to the edge would skip the track", elapsed, total)
	}

	p.Seek(-5)
	if elapsed, _ = p.Position(); elapsed != 0 {
		t.Errorf("negative seek landed at %.3f, want 0", elapsed)
	}
}

func TestManualNextWrapsRegardlessOfRepeat(t *testing.T) {
	dir := t.TempDir()
	a := writeWAV(t, dir, "a.wav", 1)
	b := writeWAV(t, dir, "b.wav", 1)

	sink := &fakeSink{}
	p, _ := New(sink)
	defer p.Close()

	p.SetQueue([]*queue.Item{{Path: a}, {Path: b}}, 1)
	p.Next() // repeat is off, but manual navigation always wraps
	if p.QueueIndex() != 0 {
		t.Errorf("index = %d, want 0 — manual Next wraps whatever the repeat mode", p.QueueIndex())
	}
}

func TestVolumeCurveAndBounds(t *testing.T) {
	sink := &fakeSink{}
	p, _ := New(sink)
	defer p.Close()

	p.SetVolume(2)
	if p.Volume() != 1 {
		t.Errorf("volume = %v, want it clamped to 1", p.Volume())
	}
	p.SetVolume(-1)
	if p.Volume() != 0 {
		t.Errorf("volume = %v, want it clamped to 0", p.Volume())
	}
	if volumeToDB(1) != 0 {
		t.Errorf("unity gain = %v, want 0", volumeToDB(1))
	}
}

// TestALocalTrackSeeksThroughTheRing is the caller-path test for the 2026-08-18
// change: local files play through the decode-ahead ring now (their decode was
// starving the pull on the phone), so a scrub must move the RING's decoder —
// position answering the target at once, and only the tail of the track left
// to play. A seek that changed the number without moving the decoder would
// pass any bookkeeping check and still play the wrong audio.
func TestALocalTrackSeeksThroughTheRing(t *testing.T) {
	dir := t.TempDir()
	path := writeWAV(t, dir, "a.wav", 0.5) // 22050 samples

	sink := &fakeSink{}
	p, err := New(sink)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	p.SetQueue([]*queue.Item{{Path: path}}, 0)
	waitPlaying(t, p)
	if b := p.src.buf; b == nil {
		t.Fatal("a local track is not playing through the ring")
	}
	sink.pump(4)

	b := p.src.buf
	p.Seek(0.4)
	if el, _ := p.Position(); el < 0.39 || el > 0.42 {
		t.Fatalf("Position right after Seek = %.3f, want ~0.4 without waiting for the fill", el)
	}

	// Let the fill land the seek — pumping earlier only collects the padding
	// the ring is entitled to while it catches up, which counts nothing.
	waitFor(t, "the fill to land the seek and drain the tail", func() bool {
		b.mu.Lock()
		defer b.mu.Unlock()
		return !b.seekPending && b.done
	})

	// Exactly the track's last 0.1s should be left: 4410 samples, then the
	// short return that ends the track. More would mean the ring kept audio
	// from before the seek; a full track's worth, that the decoder never moved.
	got := 0
	buf := make([][2]float64, 512)
	for i := 0; i < 200; i++ {
		sink.mu.Lock()
		s := sink.s
		if s == nil {
			sink.mu.Unlock()
			break
		}
		n, ok := s.Stream(buf)
		sink.mu.Unlock()
		got += n
		if !ok {
			break
		}
	}
	if got < 4410-512 || got > 4410+512 {
		t.Fatalf("pumped %d samples after seeking to 0.4 of 0.5s, want ~4410 — the seek did not reach the decoder", got)
	}
}

// TestTheSinkHearsThePauseAndIsReleasedByEveryOtherPath pins the invariant a
// device-level hold rests on: the sink is held exactly while a paused ctrl
// exists. Get it wrong in the releasing direction and the pause is merely as
// slow as it always was; get it wrong the other way and the next track plays
// to a held device — silence, with a Pause button on screen and every counter
// in the program insisting it is playing.
func TestTheSinkHearsThePauseAndIsReleasedByEveryOtherPath(t *testing.T) {
	dir := t.TempDir()
	a := writeWAV(t, dir, "a.wav", 5)
	b := writeWAV(t, dir, "b.wav", 5)

	sink := &fakeSink{}
	p, err := New(sink)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	p.SetQueue([]*queue.Item{{Path: a}, {Path: b}}, 0)
	waitPlaying(t, p)
	if sink.held() {
		t.Fatal("the sink is held while playback is running")
	}

	p.Pause()
	if !sink.held() {
		t.Fatal("the sink was not told about the pause")
	}
	p.Play()
	if sink.held() {
		t.Fatal("the sink is still held after resuming")
	}

	// Starting another track while paused: load() builds a control that is
	// NOT paused, so the hold has to go with the old one.
	p.Pause()
	if !sink.held() {
		t.Fatal("the sink was not told about the second pause")
	}
	p.SetQueue([]*queue.Item{{Path: b}}, 0)
	waitPlaying(t, p)
	if sink.held() {
		t.Fatal("a track started while paused would play to a held device")
	}

	// And the end of the queue, which is a stop rather than a pause.
	p.Pause()
	p.Stop()
	if sink.held() {
		t.Fatal("a stopped player left the device held")
	}
}
