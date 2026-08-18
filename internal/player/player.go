package player

import (
	"context"
	"errors"
	"io"
	"math"
	"sync"
	"time"

	"github.com/gopxl/beep/v2"
	"github.com/gopxl/beep/v2/effects"

	"daemonlord.ygg/madplayer/internal/queue"
)

// Fetcher turns a remote queue item into a file on this machine.
//
// It exists because the decoders leave no choice: go-mp3 walks every frame
// header before it will report a length, so "stream it" is not on the table and
// a remote track is a download that finishes before it plays. The player does
// not care where the file came from — only that it can be opened.
type Fetcher interface {
	// Local returns a complete file. It is what a caller that needs the WHOLE
	// track uses — keeping a copy on this device, for instance.
	Local(ctx context.Context, item *queue.Item) (string, error)
	// Stream returns a reader that can be decoded while the bytes are still
	// arriving, and the extension that says which decoder to use.
	//
	// It exists because the wait was the whole problem: a remote track used to
	// be silent until every byte of it had landed, which on a slow link is
	// minutes of a window that looks broken. The decoders never required that —
	// they require a reader, and only take their whole-file path when handed one
	// that seeks.
	Stream(ctx context.Context, item *queue.Item) (io.ReadCloser, string, error)
}

// Sink is the audio output.
//
// It exists so this package does not import an audio device. beep's speaker is
// a process-global with exactly this shape; the interface lets the player be
// driven by a silent test double, and keeps the one package that needs ALSA at
// the very edge of the program.
type Sink interface {
	// Init prepares the device and answers with the rate it actually runs at.
	// Everything is resampled to that answer, because a device cannot change
	// rate between tracks without a gap. The request is a preference, not a
	// contract: a phone's audio path has one native rate (48 kHz on most),
	// and feeding it anything else buys a hidden mid-quality resample inside
	// the driver — the sink that knows its device's rate returns it instead,
	// and the player aims the one resample it controls at the true target.
	Init(rate beep.SampleRate, bufferSize int) (beep.SampleRate, error)
	// Play starts a streamer. Lock/Unlock guard mutation while it is running.
	Play(s beep.Streamer)
	Lock()
	Unlock()
	// Clear stops everything currently playing.
	Clear()
	Close() error
}

// SampleRate is the rate REQUESTED of the device. 44.1 kHz is the rate most
// music is already in — but the device answers with its own rate (Sink.Init),
// and that answer, not this constant, is what playback resamples to.
const SampleRate beep.SampleRate = 44100

// resampleQuality is beep's interpolation width (quality*2 points). It was 4
// while the resample was "usually a no-op"; on a 48 kHz device it is the rule
// instead — every 44.1 kHz track crosses it — and it replaced the 8-tap
// converter oto's driver used to hide behind the sink, so it has to beat that
// converter or the native-rate change is a wash. 8 (16 points) does, for a
// cost only paid on rate-mismatched tracks.
const resampleQuality = 8

// Player owns playback AND the queue.
//
// It is the Go counterpart of the web UI's player-controller.js singleton and
// implements the same contract (docs/ui/player-and-queue.md): the queue is
// stable until a click or an explicit edit, shuffle reorders the queue itself,
// repeat affects only what happens when a track ends.
//
// The queue is deliberately NOT exposed. Playback advances it from an internal
// goroutine while the UI reads it to paint — handing out the bare queue is a
// data race, which is exactly what the race detector caught when it was public.
// Every queue operation therefore goes through a method here.
type Player struct {
	sink Sink
	// rate is what the device answered at Init — the rate every source is
	// resampled to. On the desktop it is SampleRate; on a phone it is the
	// device's native rate, so a track already in it passes through untouched.
	rate beep.SampleRate

	// qmu guards the queue. It is never held while taking mu, so the two
	// cannot deadlock against each other.
	qmu sync.Mutex
	q   *queue.Queue

	mu   sync.Mutex
	src  *source
	ctrl *beep.Ctrl
	vol  *effects.Volume
	// srcPath is the file src was decoded from. It is kept because a downloaded
	// track's bytes are only nameable AFTER the fetch: the queue item carries a
	// URL, and whatever wants to read the file itself — cover art, a tag reader
	// — needs the path the download landed at.
	srcPath string
	gen     uint64 // guards against a stale end-of-track callback, and a stale load
	volume  float64

	// fetch makes a remote item's bytes local. It is nil in an offline build of
	// the program's wiring, and a remote item then simply fails to play — which
	// is the truth.
	fetch Fetcher
	// loading is a fetch in flight, which the UI says out loud: a remote track
	// takes as long as its download, and silence with no explanation looks like
	// a hang.
	loading bool
	// cancel stops the load in flight. Starting a track must abandon the
	// previous one's download, or skipping through a queue would fetch every
	// track it passed.
	cancel context.CancelFunc
	// seekCancel stops a streaming seek in flight (seekStream). Separate from
	// cancel because they end different things: cancel is the PLAYING source's
	// reader, which a pending seek must leave alone — the track keeps sounding
	// until the seek's replacement source is ready to swap in.
	seekCancel context.CancelFunc

	// resumeKey and resumeAt are a restored queue's playback position, waiting
	// for the track it belongs to.
	//
	// They are keyed by ROW rather than being a bare number because the queue can
	// move before anybody presses play: a bare offset would seek whatever track
	// happened to load next to a position that has nothing to do with it. The key
	// is consumed on use and dropped by every explicit navigation, so a restored
	// position resumes exactly once and only where it belongs.
	resumeKey string
	resumeAt  float64

	// failed records why a track would not play, keyed by queue.Key. The contract
	// is that a media error "marks its rows unavailable and advances", so this
	// has to survive the next track succeeding — a single last-error field would
	// be wiped by the very skip it caused.
	failed  map[string]error
	lastErr error

	ended  chan uint64
	closed chan struct{}

	// OnChange is called whenever something the UI paints has changed. It runs
	// on an internal goroutine, so an implementation must be safe to call from
	// one — for Gio that means Window.Invalidate, never direct state mutation.
	OnChange func()
}

// New returns a player writing to sink.
func New(sink Sink) (*Player, error) {
	rate, err := sink.Init(SampleRate, int(SampleRate/10))
	if err != nil {
		return nil, err
	}
	if rate <= 0 {
		rate = SampleRate
	}
	p := &Player{
		sink:   sink,
		rate:   rate,
		q:      queue.New(),
		volume: 1,
		ended:  make(chan uint64, 4),
		closed: make(chan struct{}),
	}
	go p.watchEnds()
	return p, nil
}

// --- queue: reads -----------------------------------------------------------

// QueueItems returns a copy of the visible queue, safe to iterate while
// playback continues.
func (p *Player) QueueItems() []*queue.Item {
	p.qmu.Lock()
	defer p.qmu.Unlock()
	return append([]*queue.Item(nil), p.q.Items()...)
}

func (p *Player) QueueLen() int {
	p.qmu.Lock()
	defer p.qmu.Unlock()
	return p.q.Len()
}

func (p *Player) QueueIndex() int {
	p.qmu.Lock()
	defer p.qmu.Unlock()
	return p.q.Index()
}

// Current is the playing item, or nil.
func (p *Player) Current() *queue.Item {
	p.qmu.Lock()
	defer p.qmu.Unlock()
	return p.q.Current()
}

// CurrentPath is the file being decoded right now, or "" when nothing is open.
//
// It is not the same as Current().Path, and the difference is the point: a
// remote track has no path until its download finishes, and this reports the
// file the bytes actually landed in. Anything that wants to READ what is playing
// — the cover art, a tag view — needs that one and not the queue item's.
func (p *Player) CurrentPath() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.srcPath
}

func (p *Player) Shuffled() bool {
	p.qmu.Lock()
	defer p.qmu.Unlock()
	return p.q.Shuffled()
}

func (p *Player) Repeat() queue.Repeat {
	p.qmu.Lock()
	defer p.qmu.Unlock()
	return p.q.Repeat()
}

func (p *Player) QueueDirty() bool {
	p.qmu.Lock()
	defer p.qmu.Unlock()
	return p.q.Dirty()
}

func (p *Player) CanUndo() bool {
	p.qmu.Lock()
	defer p.qmu.Unlock()
	return p.q.CanUndo()
}

// Snapshot returns everything needed to persist the queue.
func (p *Player) Snapshot() (items, original []*queue.Item, index int, shuffled bool, repeat queue.Repeat) {
	p.qmu.Lock()
	defer p.qmu.Unlock()
	return append([]*queue.Item(nil), p.q.Items()...),
		append([]*queue.Item(nil), p.q.Original()...),
		p.q.Index(), p.q.Shuffled(), p.q.Repeat()
}

// --- queue: mutations -------------------------------------------------------

// SetQueue replaces the queue with a view and starts playing at index — the
// clicked view becomes the queue, in the order shown. Reports whether an undo
// of the previous queue is available, which is the caller's cue to offer it.
func (p *Player) SetQueue(items []*queue.Item, index int) bool {
	p.forgetResume()
	p.qmu.Lock()
	undo := p.q.Set(items, index)
	p.qmu.Unlock()
	p.playCurrent()
	return undo
}

// PlayNext inserts right after the current track; Append adds to the end. Both
// mark the queue dirty and neither disturbs playback.
func (p *Player) PlayNext(items ...*queue.Item) {
	p.qmu.Lock()
	p.q.PlayNext(items...)
	p.qmu.Unlock()
	p.changed()
}

func (p *Player) Append(items ...*queue.Item) {
	p.qmu.Lock()
	p.q.Append(items...)
	p.qmu.Unlock()
	p.changed()
}

// RemoveAt drops a queue position. Removing the track that is playing is a
// playback decision, so it is made here: the next track takes over.
func (p *Player) RemoveAt(i int) {
	p.qmu.Lock()
	wasCurrent := p.q.Remove(i)
	empty := p.q.Len() == 0
	p.qmu.Unlock()

	switch {
	case empty:
		p.stop()
	case wasCurrent:
		p.playCurrent()
	default:
		p.changed()
	}
}

// MoveInQueue reorders the visible queue without touching the original order.
func (p *Player) MoveInQueue(from, to int) {
	p.qmu.Lock()
	p.q.Move(from, to)
	p.qmu.Unlock()
	p.changed()
}

func (p *Player) ClearQueue() {
	p.qmu.Lock()
	p.q.Clear()
	p.qmu.Unlock()
	p.stop()
}

// ToggleShuffle transforms the queue. Playback is never interrupted in either
// direction — only the order changes, and the current track stays current.
func (p *Player) ToggleShuffle() {
	p.qmu.Lock()
	p.q.ToggleShuffle()
	p.qmu.Unlock()
	p.changed()
}

// SetShuffle puts shuffle in a given state rather than flipping it.
//
// It exists for a remote control: the desktop's media widget sends "shuffle
// on", not "shuffle the other way", and a toggle answering that would invert
// the setting whenever the two disagreed.
func (p *Player) SetShuffle(on bool) {
	p.qmu.Lock()
	change := p.q.Shuffled() != on
	if change {
		p.q.ToggleShuffle()
	}
	p.qmu.Unlock()
	if change {
		p.changed()
	}
}

// CycleRepeat steps off → all → one → off.
func (p *Player) CycleRepeat() {
	p.qmu.Lock()
	p.q.SetRepeat(p.q.Repeat().Next())
	p.qmu.Unlock()
	p.changed()
}

// SetRepeat pins the repeat mode — the same reason SetShuffle exists.
func (p *Player) SetRepeat(r queue.Repeat) {
	p.qmu.Lock()
	change := p.q.Repeat() != r
	p.q.SetRepeat(r)
	p.qmu.Unlock()
	if change {
		p.changed()
	}
}

// Stop ends playback and leaves the queue alone, which is what a remote
// control's Stop means: Play starts the same track again from the beginning.
func (p *Player) Stop() { p.stop() }

// Undo restores the queue stashed by the last replacement.
func (p *Player) Undo() bool {
	p.qmu.Lock()
	ok := p.q.Undo()
	p.qmu.Unlock()
	if ok {
		p.playCurrent()
	}
	return ok
}

// Restore revives a persisted queue WITHOUT starting playback — the web UI's
// rule, and the right one: a restored queue that started playing by itself
// would be a surprise, and pressing play is the gesture that says otherwise.
func (p *Player) Restore(items, original []*queue.Item, index int, shuffled bool, repeat queue.Repeat) {
	p.qmu.Lock()
	p.q.Restore(items, original, index, shuffled, repeat)
	p.qmu.Unlock()
	p.changed()
}

// ResumeAt arms a restored playback position for the track that is current now.
//
// It does not start anything, which is the whole point: pressing play resumes
// mid-track, and clicking a row starts that row from the beginning. Both fall
// out of one rule — the position belongs to a named row, is consumed the first
// time that row loads, and is dropped by every explicit navigation.
func (p *Player) ResumeAt(seconds float64) {
	if seconds <= 0 {
		return
	}
	cur := p.Current()
	if cur == nil {
		return
	}
	p.mu.Lock()
	p.resumeKey, p.resumeAt = cur.RowKey(), seconds
	p.mu.Unlock()
}

// forgetResume drops an armed position. Every deliberate move through the queue
// calls it: having navigated away, the saved offset is about a track the person
// is no longer asking for.
func (p *Player) forgetResume() {
	p.mu.Lock()
	p.resumeKey, p.resumeAt = "", 0
	p.mu.Unlock()
}

// --- playback ---------------------------------------------------------------

// Unplayable reports why a track would not play, or nil if it has never failed.
// The key is queue.Key of the row. The UI marks those rows rather than leaving
// the user to wonder why the queue keeps skipping.
func (p *Player) Unplayable(key string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.failed[key]
}

// TakeError pops the most recent failure so the UI can report it once. It is
// consumed rather than read, because a failure the user has already been told
// about should not keep re-announcing itself on every repaint — and silence is
// otherwise indistinguishable from a paused player.
func (p *Player) TakeError() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	err := p.lastErr
	p.lastErr = nil
	return err
}

// Toggle pauses or resumes. On a queue that has never started, it begins.
func (p *Player) Toggle() {
	p.mu.Lock()
	if p.ctrl == nil {
		loading := p.loading
		p.mu.Unlock()
		// A track already downloading has been started; pressing play again
		// must not start a second load of it.
		if !loading && p.Current() != nil {
			p.playCurrent()
		}
		return
	}
	p.sink.Lock()
	p.ctrl.Paused = !p.ctrl.Paused
	p.sink.Unlock()
	p.mu.Unlock()
	p.changed()
}

// Play and Pause set the state rather than flipping it.
//
// The on-screen button is one control that means "the other one", but a remote
// control is several: the desktop's media widget has a Play and a Pause, and a
// headset sends one of them. Answering "Play" with a toggle pauses a player that
// was already playing, which is the classic media-key bug.
func (p *Player) Play() {
	if !p.Playing() {
		p.Toggle()
	}
}

func (p *Player) Pause() {
	if p.Playing() {
		p.Toggle()
	}
}

// Playing reports whether audio is actually advancing.
func (p *Player) Playing() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.ctrl != nil && !p.ctrl.Paused
}

// Paused reports a track that is OPEN and held.
//
// It is not simply "not playing": nothing loaded, a download in flight and a
// stopped player are all not-playing and none of them is paused. The difference
// is invisible on screen — the button says Play either way — and load-bearing on
// the media bus, where Paused and Stopped are two different states and Stopped
// means the playhead went back to the start.
func (p *Player) Paused() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.ctrl != nil && p.ctrl.Paused
}

// Next and Prev are MANUAL navigation and therefore always wrap, whatever the
// repeat mode — the mode governs only what happens when a track ends by itself.
func (p *Player) Next() {
	p.forgetResume()
	p.qmu.Lock()
	ok := p.q.Next()
	p.qmu.Unlock()
	if ok {
		p.playCurrent()
	}
}

func (p *Player) Prev() {
	p.forgetResume()
	p.qmu.Lock()
	ok := p.q.Prev()
	p.qmu.Unlock()
	if ok {
		p.playCurrent()
	}
}

// PlayIndex jumps to a position in the current queue.
func (p *Player) PlayIndex(i int) {
	p.forgetResume()
	p.qmu.Lock()
	ok := i >= 0 && i < p.q.Len()
	if ok {
		p.q.Restore(p.q.Items(), p.q.Original(), i, p.q.Shuffled(), p.q.Repeat())
	}
	p.qmu.Unlock()
	if ok {
		p.playCurrent()
	}
}

// Position reports elapsed and total seconds for the current track. Both are 0
// when there is no current track at all.
//
// A streaming mp3 has no length of its own — go-mp3 only computes one by
// walking the whole file, which is exactly what a stream refuses to do. The
// library knows it anyway, so the total falls back to the queue item's
// duration. Living here rather than in the player bar means every consumer of
// the total — the bar, the keyboard's relative seek, the media bus — agrees on
// it, and a scrub on a streaming track has a length to aim into.
//
// Nothing DECODED yet is answered the same way, from what the queue already
// knows: a restored queue sits on a track it has not opened, and reporting 0:00
// of 0:00 for a five-minute song made a resumed session look like an empty one.
// An armed resume position is where that track will start, so it is where the
// playhead is — the bar shows the point the last session stopped at, before a
// single byte is read. Seekable stays false throughout: there is a length to
// read, and still nothing open to scrub.
func (p *Player) Position() (elapsed, total float64) {
	cur := p.Current()
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.src == nil {
		if cur == nil {
			return 0, 0
		}
		if p.resumeKey != "" && p.resumeKey == cur.RowKey() {
			elapsed = p.resumeAt
		}
		return elapsed, cur.Duration
	}
	var pos, length int
	if b := p.src.buf; b != nil {
		// A streaming source answers from its ring's own bookkeeping, and
		// deliberately NOT under the sink lock: the audio pull may be inside a
		// Stream call at any moment, and this method runs on the UI's frame.
		// Before the ring existed, that lock was held across a decoder read
		// that could park on the network — which froze the window for as long
		// as the download stalled (the Android ANR of 2026-08-17).
		pos, length = b.Position(), b.Len()
	} else {
		p.sink.Lock()
		pos, length = p.src.streamer.Position(), p.src.streamer.Len()
		p.sink.Unlock()
	}
	pos += p.src.base // a mid-track source counts from where its bytes began

	rate := float64(p.src.format.SampleRate)
	if rate <= 0 {
		return 0, 0
	}
	elapsed, total = float64(pos)/rate, float64(length)/rate
	if total <= 0 && cur != nil {
		total = cur.Duration
	}
	return elapsed, total
}

// Seekable reports whether the track playing right now can be scrubbed.
//
// Any open track can: a local file natively, and a track still arriving via
// seekStream, which restarts its decode at the target rather than ever calling
// the decoder's own Seek — beep's mp3 decoder PANICS on a non-seekable source,
// and that guard now lives inside Seek instead of disabling the bar.
func (p *Player) Seekable() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.src != nil
}

// Seek moves to a position in seconds, clamped to the track.
//
// A seekable source (a local or fully cached file) seeks in place. One still
// arriving cannot — its decoder chose the streaming path precisely because its
// reader does not seek, and beep's mp3 Seek panics on such a source — so the
// scrub is answered by seekStream: reopen the growing file and decode forward
// to the target. The web UI gets the same result from the browser plus the
// relay's Range support; this is the native client's spelling of it.
func (p *Player) Seek(seconds float64) {
	if seconds < 0 {
		seconds = 0
	}
	p.mu.Lock()
	if p.src == nil {
		p.mu.Unlock()
		return
	}
	if !p.src.seekable {
		p.mu.Unlock()
		p.seekStream(seconds)
		return
	}
	rate := float64(p.src.format.SampleRate)
	target := int(seconds * rate)

	p.sink.Lock()
	// Seeking exactly to the end would end the track, turning a scrub to the
	// right-hand edge into a skip. Stop one sample short.
	if n := p.src.streamer.Len(); target >= n {
		target = n - 1
	}
	if target >= 0 {
		_ = p.src.streamer.Seek(target)
	}
	p.sink.Unlock()
	p.mu.Unlock()
}

// seekStream scrubs a track whose bytes are still arriving.
//
// The mechanism is restart-with-skip: open a SECOND reader over the same
// growing cache file — joining the running fetch as one more waiter, never a
// second download — decode forward to the target discarding samples, and swap
// the finished source in. Decoding runs far faster than realtime, so a scrub
// into what has already arrived lands quickly; one beyond the watermark waits
// for the download to reach it, the old position audibly playing on until the
// swap. The decoder's own Seek is never called, so the mp3 panic is
// structurally unreachable.
//
// The swap bumps gen, exactly like a track change: the superseded source's
// end-of-track callback goes stale, a newer scrub obsoletes an older one still
// skipping, and a track change obsoletes them all.
func (p *Player) seekStream(seconds float64) {
	item := p.Current()
	p.mu.Lock()
	if item == nil || p.fetch == nil || p.src == nil || p.src.seekable {
		p.mu.Unlock()
		return
	}
	p.gen++
	gen := p.gen
	if p.seekCancel != nil {
		p.seekCancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	p.seekCancel = cancel
	// Say something while it works: a scrub past the watermark takes as long
	// as the download, and a thumb that snaps back with no explanation reads
	// as a refusal.
	p.loading = true
	p.mu.Unlock()
	p.changed()

	go func() {
		src, path, err := p.openItemAt(ctx, item, seconds)
		if err == nil {
			// Same rule as load: the ring goes on when the source is about to
			// reach the sink, and only then (openItemAt's discardTo counted
			// real samples through the raw decoder to land this seek).
			src.bufferAhead()
		}
		p.mu.Lock()
		// p.src can be nil here (Stop won the race): a seek of a stopped player
		// must not resurrect playback.
		if gen != p.gen || p.src == nil {
			p.mu.Unlock()
			src.Close()
			cancel()
			return
		}
		p.loading = false
		p.seekCancel = nil
		if err != nil {
			// A failed scrub is not a failed track: whatever was playing keeps
			// playing where it was.
			p.mu.Unlock()
			cancel()
			p.changed()
			return
		}

		p.sink.Lock()
		paused := p.ctrl != nil && p.ctrl.Paused
		p.sink.Unlock()

		old, oldCancel := p.src, p.cancel
		p.src, p.srcPath = src, path
		p.cancel = cancel
		streamer := beep.Streamer(src.streamer)
		if src.format.SampleRate != p.rate {
			streamer = beep.Resample(resampleQuality, src.format.SampleRate, p.rate, streamer)
		}
		p.ctrl = &beep.Ctrl{Streamer: streamer, Paused: paused}
		p.vol = &effects.Volume{Streamer: p.ctrl, Base: 2, Volume: volumeToDB(p.volume), Silent: p.volume == 0}
		p.sink.Clear()
		p.sink.Play(beep.Seq(p.vol, beep.Callback(func() {
			select {
			case p.ended <- gen:
			default:
			}
		})))
		p.mu.Unlock()

		// The replaced source is retired AFTER the swap, so the audio never
		// gaps on a closed reader. Its context goes first — the cancel is what
		// wakes a fill goroutine parked on a stalled download, and the close
		// is only a signal to it (buffered.Close).
		if oldCancel != nil {
			oldCancel()
		}
		old.Close()
		p.changed()
	}()
}

// openItemAt is openItem continued to a position: the source comes back
// standing at the requested second.
//
// Three ways there, cheapest honest one first. A local or fully cached file
// seeks natively. A still-arriving mp3 or flac starts MID-STREAM: a Range
// request fetches the seeked region first — the same move the web UI's
// browser makes against the relay — and the decoder picks up there
// (rangeseek.go). Everything else decodes its way to the target through the
// sequential fill (discardTo), which also catches every range-path failure:
// losing the fast path must never lose the seek.
func (p *Player) openItemAt(ctx context.Context, item *queue.Item, seconds float64) (*source, string, error) {
	src, path, err := p.openItem(ctx, item)
	if err != nil {
		return nil, "", err
	}
	rate := float64(src.format.SampleRate)
	target := int(seconds * rate)
	if target <= 0 {
		return src, path, nil
	}
	if src.seekable {
		// A position at or past the end is IMPOSSIBLE, not far: it is ignored
		// and the track starts at the top — the resume contract, and the sane
		// answer to a stale duration estimate. Scrubs never get here with one
		// (the slider is bounded by the total, and the range path clamps).
		if target < src.streamer.Len() {
			_ = src.streamer.Seek(target)
		}
		return src, path, nil
	}
	if ranged, rerr := p.openRanged(ctx, item, seconds, src); rerr == nil {
		// src stays OPEN underneath, deliberately: its reader is what keeps the
		// background fill — the cache's copy, the household's seed — counted as
		// wanted. It closes when the ranged source does.
		ranged.closer = stackedCloser{ranged.closer, src}
		return ranged, "", nil
	}
	if err := discardTo(src.streamer, target); err != nil {
		src.Close()
		return nil, "", err
	}
	return src, path, nil
}

// stackedCloser closes a source's own reader and then whatever it was keeping
// alive underneath.
type stackedCloser [2]io.Closer

func (s stackedCloser) Close() error {
	err := s[0].Close()
	if e := s[1].Close(); err == nil {
		err = e
	}
	return err
}

// discardTo decodes and throws away samples until the streamer stands at n.
//
// Stopping early at a clean end is not an error: a scrub past the end of the
// track hands the sink an exhausted streamer, which it turns into an ordinary
// end-of-track — the same thing seeking to the last instant means.
func discardTo(s beep.StreamSeekCloser, n int) error {
	buf := make([][2]float64, 1024)
	for n > 0 {
		want := len(buf)
		if n < want {
			want = n
		}
		got, ok := s.Stream(buf[:want])
		n -= got
		if !ok {
			return s.Err()
		}
	}
	return nil
}

// SetVolume sets the output level, 0..1, on a perceptual curve — a linear fader
// spends most of its travel in a range that sounds like nothing is happening.
func (p *Player) SetVolume(v float64) {
	if v < 0 {
		v = 0
	}
	if v > 1 {
		v = 1
	}
	p.mu.Lock()
	p.volume = v
	if p.vol != nil {
		p.sink.Lock()
		p.vol.Volume = volumeToDB(v)
		p.vol.Silent = v == 0
		p.sink.Unlock()
	}
	p.mu.Unlock()
	p.changed()
}

func (p *Player) Volume() float64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.volume
}

// volumeToDB maps 0..1 onto beep's base-2 exponent, where 0 is unity gain and
// each -1 halves the amplitude.
func volumeToDB(v float64) float64 {
	if v <= 0 {
		return -8
	}
	return math.Log2(v) * 2
}

// playCurrent starts whatever the queue currently points at.
//
// Opening is done on a goroutine, not here, because a remote track has to be
// downloaded first and this is called straight from a click handler — resolving
// inline would freeze the window for the length of a download. The generation
// counter that already guarded stale end-of-track callbacks does the same job
// for a load that finishes after the user moved on.
func (p *Player) playCurrent() {
	item := p.Current()
	if item == nil {
		p.stop()
		return
	}

	p.mu.Lock()
	// The old download is abandoned FIRST, before anything touches the sink or
	// the source: a fill goroutine parked at the tail of a stalled download
	// wakes on this cancellation and on nothing else, and it is what lets the
	// close below be a signal instead of a wait.
	if p.cancel != nil {
		p.cancel()
	}
	if p.seekCancel != nil {
		// A pending scrub of the old track dies with it — its goroutine would
		// otherwise sit on the old download as a waiter forever, keeping a
		// fetch alive that nobody will ever hear.
		p.seekCancel()
		p.seekCancel = nil
	}
	p.sink.Clear()
	p.src.Close()
	p.src, p.ctrl, p.vol, p.srcPath = nil, nil, nil, ""
	p.gen++
	gen := p.gen
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	// Only a download is worth announcing. Opening a local file is fast enough
	// that a "loading" flash would be noise.
	p.loading = item.Remote()
	p.mu.Unlock()
	p.changed()

	go p.load(ctx, gen, item)
}

// load resolves an item to a file, decodes it and installs it as the playing
// source — unless the queue moved on while it was working.
func (p *Player) load(ctx context.Context, gen uint64, item *queue.Item) {
	// A restored position is consumed HERE, before the open, and openItemAt
	// carries the source to it — natively for a file, mid-stream or by
	// decoding forward for one still arriving. Consumed whether it lands or
	// not, so a track that will not seek does not keep trying; and outside the
	// lock, because reaching the position can wait on the network.
	at := 0.0
	p.mu.Lock()
	if gen == p.gen && p.resumeKey != "" && p.resumeKey == item.RowKey() {
		at = p.resumeAt
		p.resumeKey, p.resumeAt = "", 0
	}
	p.mu.Unlock()

	var src *source
	var path string
	var err error
	if at > 0 {
		src, path, err = p.openItemAt(ctx, item, at)
	} else {
		src, path, err = p.openItem(ctx, item)
	}
	if err == nil {
		// A still-arriving track plays through the decode-ahead ring from here
		// on: the sink must never run a decoder that can block on the network.
		// After openItemAt, on purpose — its seek work needs the raw decoder.
		src.bufferAhead()
	}

	p.mu.Lock()
	if gen != p.gen {
		// Superseded: something else is playing now. Drop what was opened rather
		// than letting two tracks reach the sink.
		p.mu.Unlock()
		src.Close()
		return
	}
	p.loading = false

	if err != nil {
		// A cancelled load is not a broken track — it is the user skipping past
		// it — so it neither marks the row nor advances the queue.
		if errors.Is(err, context.Canceled) {
			p.mu.Unlock()
			p.changed()
			return
		}
		if p.failed == nil {
			p.failed = make(map[string]error)
		}
		p.failed[item.RowKey()] = err
		p.lastErr = err
		p.mu.Unlock()
		p.changed()
		// A file that will not open is not a reason to stop the queue — it is a
		// reason to move past it. errored=true also suppresses repeat-one, so a
		// broken file cannot loop forever.
		p.advance(true)
		return
	}

	p.src, p.srcPath = src, path
	delete(p.failed, item.RowKey()) // it played this time

	streamer := beep.Streamer(src.streamer)
	if src.format.SampleRate != p.rate {
		streamer = beep.Resample(resampleQuality, src.format.SampleRate, p.rate, streamer)
	}
	p.ctrl = &beep.Ctrl{Streamer: streamer}
	p.vol = &effects.Volume{Streamer: p.ctrl, Base: 2, Volume: volumeToDB(p.volume), Silent: p.volume == 0}

	// Handing to the sink while still holding mu is deliberate: two loads can
	// now finish at once, and releasing first would let a superseded one start
	// playing after the winner already had. The lock order is always mu → sink,
	// here as everywhere else in this file, so it cannot deadlock.
	p.sink.Play(beep.Seq(p.vol, beep.Callback(func() {
		// Runs on the sink's goroutine: hand off rather than doing work here,
		// where taking the sink lock would deadlock.
		select {
		case p.ended <- gen:
		default:
		}
	})))
	p.mu.Unlock()
	p.changed()
}

// resolve finds the file to decode. A local path is used as it is; anything else
// is a download.
// openItem decodes an item, from this machine's disk or from a download that has
// only just started.
//
// The second result is the local path when there is one, and "" while a track is
// still arriving — the cover art reads it, and a track being streamed has no
// path anybody should show yet.
func (p *Player) openItem(ctx context.Context, item *queue.Item) (*source, string, error) {
	if item.Path != "" {
		src, err := open(item.Path)
		return src, item.Path, err
	}
	// Not "has a URL": a madnetwork track has none, and is fetched by content
	// hash from whoever holds it. The item itself is what knows whether it names
	// audio at all.
	if !item.Playable() {
		return nil, "", errors.New("this track has no audio to play")
	}
	p.mu.Lock()
	fetch := p.fetch
	p.mu.Unlock()
	if fetch == nil {
		return nil, "", errors.New("this track is on a server, and no connection is configured")
	}

	// Streamed, not waited for. A reader that blocks at the tail of a growing
	// file lets the decoder start on the first fraction of a percent of it, which
	// is the difference between a track that plays now and one that plays in four
	// minutes.
	rc, ext, err := fetch.Stream(ctx, item)
	if err != nil {
		return nil, "", err
	}
	src, err := openReader(rc, ext)
	if err != nil {
		rc.Close()
		return nil, "", err
	}
	// A cached track comes back seekable, and then it has a path like any other.
	path := ""
	if src.seekable {
		if f, ok := rc.(interface{ Name() string }); ok {
			path = f.Name()
		}
	}
	return src, path, nil
}

// SetFetcher installs what downloads remote tracks. Without one, a remote item
// says so rather than failing obscurely.
func (p *Player) SetFetcher(f Fetcher) {
	p.mu.Lock()
	p.fetch = f
	p.mu.Unlock()
}

// Loading reports a download in flight for the current track. The UI says so:
// waiting with no explanation is indistinguishable from a hang.
func (p *Player) Loading() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.loading
}

// watchEnds turns end-of-track callbacks into queue advances.
func (p *Player) watchEnds() {
	for {
		select {
		case <-p.closed:
			return
		case gen := <-p.ended:
			p.mu.Lock()
			stale := gen != p.gen
			p.mu.Unlock()
			if stale {
				continue // a track we already replaced; not an end
			}
			p.advance(false)
		}
	}
}

// advance applies the repeat rules after a track finishes.
func (p *Player) advance(errored bool) {
	p.qmu.Lock()
	more := p.q.TrackEnded(errored)
	p.qmu.Unlock()
	if !more {
		p.stop()
		return
	}
	p.playCurrent()
}

func (p *Player) stop() {
	p.mu.Lock()
	// Cancellation first, close after, same as playCurrent: the cancel is what
	// wakes a fill goroutine parked on a stalled download, and the source's
	// close is only a signal to it.
	if p.cancel != nil {
		p.cancel() // whatever was downloading, nobody is waiting for it now
		p.cancel = nil
	}
	if p.seekCancel != nil {
		p.seekCancel() // same for a scrub still working its way there
		p.seekCancel = nil
	}
	p.sink.Clear()
	p.src.Close()
	p.src, p.ctrl, p.vol, p.srcPath = nil, nil, nil, ""
	p.gen++
	p.loading = false
	p.mu.Unlock()
	p.changed()
}

func (p *Player) changed() {
	if p.OnChange != nil {
		p.OnChange()
	}
}

// Close stops playback and releases the device.
func (p *Player) Close() error {
	select {
	case <-p.closed:
		return nil // already closed
	default:
	}
	close(p.closed)
	p.stop()
	return p.sink.Close()
}

// Tick is the repaint interval a UI should use while playing.
func (p *Player) Tick() time.Duration { return 200 * time.Millisecond }
