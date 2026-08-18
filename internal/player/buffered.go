package player

// Decoding ahead of the audio device, for a track whose bytes are still
// arriving.
//
// Without this the decode ran INSIDE the device's pull: the sink calls
// Stream, Stream runs the decoder, and the decoder reads from a reader that
// blocks at the tail of a growing file (blobcache.tail) until the download
// delivers more bytes. Every one of those calls happens with the sink lock
// held — and the UI takes that lock through Position() on every frame. So a
// network stall parked the audio goroutine on a condition variable while it
// held the lock, the frame handler queued behind it, Gio's Android callback
// never returned, and after five seconds of undelivered touches the system
// declared the whole program unresponsive. Seen live on the phone,
// 2026-08-17: main thread in GioView.onFrameCallback, sound still coming out
// of a track that trickled.
//
// The fix is structural rather than a timeout: a fill goroutine owns the
// decoder and runs it OFF every lock, into a ring holding several seconds of
// samples. What the sink pulls is only ever a memory copy. A stall now costs
// silence — the ring runs dry and the stream pads — instead of wedging every
// lock between the network and the screen. The same window also absorbs the
// scheduling jitter that used to turn a busy phone into crackle: the pull no
// longer needs the decoder to hit a real-time deadline, only the copy. And
// where the ring DOES run out, the gap is made clean rather than crackly:
// delivery holds until rearmWindow is buffered again (a trickle is not
// dribbled out with silence between), and both edges of the gap are ramped.
//
// EVERY source is wrapped now (source.bufferAhead), and the two kinds are
// wrapped for two different reasons. A streaming source, because its decoder
// can block on the network — the ANR above. A LOCAL file, because decoding
// is not free either: on the Pixel 7 Pro the audio feed goroutine lands on a
// little core, where FLAC decode plus the quality-8 resample ran at barely
// half of realtime — the Debugging page caught it on 2026-08-18 as a train
// of "slow pull" lines ending in a starved feed, audible as the crackle the
// owner had been reporting all along. Same disease as the network stall,
// milder strain: work inside the pull. Same cure: the ring.
//
// The wrap still happens only when the source is installed for playback:
// rangeseek's base calibration and openItemAt's discardTo must talk to the
// raw decoder, because a ring that pads silence would let a seek count
// padding as audio and land short of its target.
//
// A seekable source keeps its scrub: Seek works on the wrapper (the player
// clamps against Len first, exactly as it did against the raw decoder). The
// request is carried out by the fill goroutine, which owns the decoder — the
// caller does not wait for it. Position answers the target immediately, the
// ring is flushed, and a fill already mid-decode when the seek lands has its
// samples discarded by a generation check rather than played out of place.

import (
	"errors"
	"sync"
	"time"

	"github.com/gopxl/beep/v2"
)

const (
	// aheadWindow is how much decoded audio the ring holds. It has to ride out
	// the pauses a swarm fetch actually has — a holder going quiet between
	// chunks is seconds, not milliseconds (ChunkStall alone tolerates 20) —
	// plus the CPU spikes chunk hashing and overlay crypto put next to the
	// decoder on a phone. Two seconds proved too tight in exactly that spot,
	// heard as occasional crackle on remote tracks (2026-08-17). Eight seconds
	// of stereo float64 is ~5.6 MB, dropped whole at track change.
	aheadWindow = 8 * time.Second

	// rearmWindow is how much the ring must refill before audio resumes after
	// running dry. Without it a slow trickle is delivered the moment it
	// arrives, a few hundred samples at a time with silence padded between —
	// audio and silence alternating at buffer rate, which is heard as crackle.
	// Gated, one dry spell is one gap: one artifact instead of dozens.
	rearmWindow = 500 * time.Millisecond

	// fillChunk is how many samples the fill decodes per call — the same 512
	// beep's own mixer streams in, so one decode step and one mix step move
	// the same amount of audio.
	fillChunk = 512

	// declickLen is the ramp, in samples (~3 ms at 44.1 kHz), applied where
	// real audio meets ring padding. A hard cut to or from silence is a click,
	// and a dry spell has two such edges; the ramp runs over the real audio on
	// each side of the gap.
	declickLen = 128
)

// buffered is a beep.StreamSeekCloser that serves from a ring a background
// goroutine keeps full. Its Stream never blocks and never returns short while
// the track is alive — both halves are load-bearing:
//
//   - never blocks: Stream runs under the sink lock, which the UI needs every
//     frame. Anything slower than a copy here is the freeze this type exists
//     to remove.
//   - never short: beep's Mixer drops a streamer that returns fewer samples
//     than asked even with ok=true, and its Resampler reads a short fill as
//     end-of-data. A stall answered "honestly" would therefore END the track;
//     it has to be silence instead.
type buffered struct {
	// inner is the real decoder. The fill goroutine OWNS it: nothing else may
	// touch it once the goroutine starts, and it is the goroutine that closes
	// it on the way out — Close from another goroutine racing a Stream call in
	// progress is exactly the kind of decoder-internal race this avoids.
	inner beep.StreamSeekCloser

	// mu guards the ring and the flags. It is only ever held for memory
	// copies — the whole point — so anyone may take it from any goroutine
	// without inheriting the network's schedule. The fill waits on cond for
	// space; Stream and Close broadcast.
	mu    sync.Mutex
	cond  *sync.Cond
	ring  [][2]float64
	start int // first undelivered sample
	count int // how many the ring holds
	// done is the fill finished — the decoder drained or failed — and err is
	// what it had to say. The stream ends when done AND the ring is empty:
	// the last buffered samples still belong to the listener.
	done bool
	err  error
	// closed is Close having been called. The fill exits at the next
	// opportunity; a fill parked inside a blocking read is woken by the load
	// context's cancellation, which every caller performs around Close.
	closed bool

	// rearm and starved carry the dry-spell hysteresis: once Stream has had to
	// pad, starved holds delivery silent until the ring holds rearm samples
	// again (or the track is done and owed its last ones) — see rearmWindow.
	rearm   int
	starved bool

	// canSeek is whether the inner decoder may be Seeked at all — beep's mp3
	// decoder PANICS on a non-seekable reader, so this is a property of the
	// source, snapshotted at wrap time. seekTo/seekPending is a request the
	// fill goroutine carries out (it owns the decoder); gen counts seeks so a
	// fill that was already mid-decode when one landed can recognise its
	// samples as pre-seek audio and drop them.
	canSeek     bool
	seekTo      int
	seekPending bool
	gen         int

	// pos is the playhead in samples: the decoder's position at wrap time plus
	// every REAL sample delivered since. Padding does not count — silence the
	// network owes is not audio that played — so a stalled track's bar stands
	// still, which is the truth. Starting from the decoder's own position is
	// what keeps a mid-stream source honest: rangeseek calibrates base against
	// that value, and a wrapper that reset it to zero would shear every ranged
	// seek by the primed amount.
	pos int64
	// length is the decoder's Len at wrap time. Snapshot rather than delegated
	// because the fill owns the decoder — and for these sources it never
	// changes: flac's comes from STREAMINFO, a streaming mp3 reports none.
	length int

	// exited closes when the fill goroutine is gone and the decoder closed —
	// what a test synchronises on, since Close deliberately does not wait.
	exited chan struct{}
}

// newBuffered wraps a decoder and starts its fill. From this moment the
// decoder belongs to the fill goroutine.
func newBuffered(inner beep.StreamSeekCloser, rate beep.SampleRate, canSeek bool) *buffered {
	n := rate.N(aheadWindow)
	if n <= 0 {
		n = int(SampleRate) * 2
	}
	b := &buffered{
		inner:   inner,
		ring:    make([][2]float64, n),
		length:  inner.Len(),
		pos:     int64(inner.Position()),
		canSeek: canSeek,
		exited:  make(chan struct{}),
	}
	b.rearm = rate.N(rearmWindow)
	if b.rearm <= 0 || b.rearm > n/2 {
		b.rearm = n / 2
	}
	b.cond = sync.NewCond(&b.mu)
	go b.fill()
	return b
}

// fill runs the decoder into the ring until the track ends or Close asks it
// to stop. The decode itself happens with NO lock held: it is the one call in
// the program allowed to sit on the network, and the price of that privilege
// is owning nothing anybody else waits on.
func (b *buffered) fill() {
	defer func() {
		// The decoder is the goroutine's to close (see buffered.inner). Its
		// own reader closes underneath it, as it did before this type existed.
		_ = b.inner.Close()
		close(b.exited)
	}()

	buf := make([][2]float64, fillChunk)
	for {
		b.mu.Lock()
		// Park while there is nothing to do: ring full, or the decoder drained
		// on a source that can seek — where "drained" is not the end of the
		// goroutine's life, because a scrub backwards would need it again.
		for !b.closed && !b.seekPending && (b.count == len(b.ring) || b.done) {
			b.cond.Wait()
		}
		if b.closed {
			b.mu.Unlock()
			return
		}
		if b.seekPending {
			target := b.seekTo
			gen := b.gen
			b.seekPending = false
			b.mu.Unlock()
			err := b.inner.Seek(target)
			b.mu.Lock()
			// A later seek supersedes this one, outcome included.
			if err != nil && b.gen == gen {
				// Playing on from wherever the decoder ended up would put the
				// audio somewhere the bar does not show; ending the track is
				// the honest answer, and the queue carries on from it.
				b.done, b.err = true, err
			}
			b.mu.Unlock()
			continue
		}
		want := min(len(buf), len(b.ring)-b.count)
		gen := b.gen
		b.mu.Unlock()

		// Space only grows while the lock is down — the fill is the sole
		// producer — so want is still available when the samples come back.
		got, ok := b.inner.Stream(buf[:want])

		b.mu.Lock()
		if b.gen != gen {
			// A seek landed while the decoder was streaming: these samples are
			// from before it, and the ring was already flushed of their kind.
			b.mu.Unlock()
			continue
		}
		for i := 0; i < got; {
			at := (b.start + b.count) % len(b.ring)
			n := min(got-i, len(b.ring)-at)
			copy(b.ring[at:at+n], buf[i:i+n])
			b.count += n
			i += n
		}
		if !ok {
			b.done, b.err = true, b.inner.Err()
		}
		closed := b.closed
		b.mu.Unlock()
		if closed {
			return
		}
		if !ok && !b.canSeek {
			// A drained stream is truly over — its bytes cannot be revisited —
			// so the goroutine's work is done. A drained FILE parks in the wait
			// above instead, keeping the decoder alive for a scrub backwards.
			return
		}
	}
}

// Stream serves what the ring holds and pads the rest with silence. See the
// type comment for why it must neither block nor return short mid-track.
func (b *buffered) Stream(samples [][2]float64) (int, bool) {
	b.mu.Lock()
	got := 0
	// A starved stream stays silent until the ring rearms — unless the track
	// is done, in which case what remains is its last audio and is owed.
	gated := b.starved && !b.done && !b.closed && b.count < b.rearm
	if !gated {
		for b.count > 0 && got < len(samples) {
			n := min(b.count, len(samples)-got, len(b.ring)-b.start)
			copy(samples[got:got+n], b.ring[b.start:b.start+n])
			b.start = (b.start + n) % len(b.ring)
			b.count -= n
			got += n
		}
	}
	resumed := b.starved && got > 0
	if got > 0 {
		b.starved = false
	}
	b.pos += int64(got)
	ended := (b.done || b.closed) && b.count == 0
	// The ring ran dry with the track still alive: the network is behind.
	// Silence, counted by nobody — pos stands still until real audio moves it.
	padding := got < len(samples) && !ended
	if padding {
		b.starved = true
	}
	b.mu.Unlock()
	if got > 0 {
		b.cond.Broadcast() // room freed; the fill may be waiting for it
	}

	if resumed {
		fadeIn(samples[:got])
	}
	if padding {
		fadeOut(samples[:got])
		clear(samples[got:])
		return len(samples), true
	}
	if got == len(samples) {
		return got, true
	}
	// The decoder is finished and the ring is drained: these are the track's
	// last samples, and the short return is the end-of-track signal that runs
	// the queue.
	return got, false
}

// fadeIn ramps the head of the first real audio after a dry spell up from
// near-silence, over at most declickLen samples.
func fadeIn(s [][2]float64) {
	n := min(declickLen, len(s))
	for i := 0; i < n; i++ {
		g := float64(i+1) / float64(n+1)
		s[i][0] *= g
		s[i][1] *= g
	}
}

// fadeOut ramps the tail of the real audio before a pad down to exactly zero,
// over at most declickLen samples, so the padding continues it seamlessly.
func fadeOut(s [][2]float64) {
	n := min(declickLen, len(s))
	for i := 0; i < n; i++ {
		g := float64(n-1-i) / float64(n)
		s[len(s)-n+i][0] *= g
		s[len(s)-n+i][1] *= g
	}
}

// Position is the playhead in samples — see buffered.pos for what counts.
func (b *buffered) Position() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return int(b.pos)
}

// Len is the total the decoder knew at wrap time, and 0-or-less when it knew
// none — the player falls back to the library's duration exactly as before.
func (b *buffered) Len() int { return b.length }

// Err is why the stream ended, when it ended badly.
func (b *buffered) Err() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.err
}

// Seek asks the fill to move a seekable decoder, without waiting for it.
//
// The caller holds the sink lock (player.Seek), the fill may be mid-decode,
// and the decoder belongs to the fill — so the request is flags, not a call.
// Everything observable moves NOW: the position answers the target, the ring
// forgets the audio it held (it is from the wrong place), and the generation
// steps so a decode already in flight is discarded rather than delivered.
// What plays until the fill lands the seek and refills is the same brief
// silence the dry-spell path already knows how to pad.
//
// On a non-seekable source it stays what it always was — structurally
// unreachable, answered with an error rather than beep's mp3 panic: the
// player scrubs those with seekStream, never the streamer's own Seek.
func (b *buffered) Seek(n int) error {
	if !b.canSeek {
		return errors.New("a buffered stream does not seek")
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.seekTo, b.seekPending = n, true
	b.gen++
	b.count = 0
	b.pos = int64(n)
	b.done, b.err = false, nil
	b.mu.Unlock()
	b.cond.Broadcast() // the fill may be parked on a full ring or a drained decoder
	return nil
}

// Close asks the fill to stop and returns WITHOUT waiting for it. It is
// called with the player's lock held (source.Close under playCurrent and
// stop), and a fill parked inside a blocked read only wakes when the load
// context is cancelled — waiting here would re-create the very wedge this
// type removes. The callers all cancel that context around the close; the
// decoder is closed by the fill on its way out.
func (b *buffered) Close() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	b.mu.Unlock()
	b.cond.Broadcast()
	return nil
}

// bufferAhead moves a source's decode off the audio pull, onto a ring a
// goroutine keeps full. Called exactly once, when the source is about to be
// installed for playback — never earlier, because rangeseek's base
// calibration and openItemAt's discardTo need the raw decoder (a padding ring
// would count silence as skipped audio and land a seek short).
//
// Local files are wrapped too, since 2026-08-18: "reads from a finished file
// on disk" turned out to mean "runs a FLAC decode on whichever core the
// phone felt like", and that was the crackle — see the file comment.
func (s *source) bufferAhead() {
	if s == nil || s.buf != nil {
		return
	}
	s.buf = newBuffered(s.streamer, s.format.SampleRate, s.seekable)
	s.streamer = s.buf
}
