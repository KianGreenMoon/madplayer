package player

import (
	"math"
	"sync"
	"time"

	"github.com/gopxl/beep/v2"
	"github.com/gopxl/beep/v2/effects"

	"daemonlord.ygg/madplayer/internal/queue"
)

// Sink is the audio output.
//
// It exists so this package does not import an audio device. beep's speaker is
// a process-global with exactly this shape; the interface lets the player be
// driven by a silent test double, and keeps the one package that needs ALSA at
// the very edge of the program.
type Sink interface {
	// Init prepares the device at a fixed sample rate. Everything is resampled
	// to it, because a device cannot change rate between tracks without a gap.
	Init(rate beep.SampleRate, bufferSize int) error
	// Play starts a streamer. Lock/Unlock guard mutation while it is running.
	Play(s beep.Streamer)
	Lock()
	Unlock()
	// Clear stops everything currently playing.
	Clear()
	Close() error
}

// SampleRate is the device rate everything resamples to. 44.1 kHz is the rate
// most music is already in, so the common case resamples by a factor of one.
const SampleRate beep.SampleRate = 44100

// resampleQuality is beep's filter width. 4 is its documented "good enough for
// music" setting; higher costs CPU for a difference nobody hears on a resample
// that is usually a no-op anyway.
const resampleQuality = 4

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

	// qmu guards the queue. It is never held while taking mu, so the two
	// cannot deadlock against each other.
	qmu sync.Mutex
	q   *queue.Queue

	mu     sync.Mutex
	src    *source
	ctrl   *beep.Ctrl
	vol    *effects.Volume
	gen    uint64 // guards against a stale end-of-track callback
	volume float64

	// failed records why a track would not play, keyed by path. The contract is
	// that a media error "marks its rows unavailable and advances", so this has
	// to survive the next track succeeding — a single last-error field would be
	// wiped by the very skip it caused.
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
	if err := sink.Init(SampleRate, int(SampleRate/10)); err != nil {
		return nil, err
	}
	p := &Player{
		sink:   sink,
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

// CycleRepeat steps off → all → one → off.
func (p *Player) CycleRepeat() {
	p.qmu.Lock()
	p.q.SetRepeat(p.q.Repeat().Next())
	p.qmu.Unlock()
	p.changed()
}

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

// --- playback ---------------------------------------------------------------

// Unplayable reports why a track would not play, or nil if it has never failed.
// The UI marks those rows rather than leaving the user to wonder why the queue
// keeps skipping.
func (p *Player) Unplayable(path string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.failed[path]
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
		p.mu.Unlock()
		if p.Current() != nil {
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

// Playing reports whether audio is actually advancing.
func (p *Player) Playing() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.ctrl != nil && !p.ctrl.Paused
}

// Next and Prev are MANUAL navigation and therefore always wrap, whatever the
// repeat mode — the mode governs only what happens when a track ends by itself.
func (p *Player) Next() {
	p.qmu.Lock()
	ok := p.q.Next()
	p.qmu.Unlock()
	if ok {
		p.playCurrent()
	}
}

func (p *Player) Prev() {
	p.qmu.Lock()
	ok := p.q.Prev()
	p.qmu.Unlock()
	if ok {
		p.playCurrent()
	}
}

// PlayIndex jumps to a position in the current queue.
func (p *Player) PlayIndex(i int) {
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
// when nothing is loaded.
func (p *Player) Position() (elapsed, total float64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.src == nil {
		return 0, 0
	}
	p.sink.Lock()
	pos, length := p.src.streamer.Position(), p.src.streamer.Len()
	p.sink.Unlock()

	rate := float64(p.src.format.SampleRate)
	if rate <= 0 {
		return 0, 0
	}
	return float64(pos) / rate, float64(length) / rate
}

// Seek moves to a position in seconds, clamped to the track.
func (p *Player) Seek(seconds float64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.src == nil {
		return
	}
	rate := float64(p.src.format.SampleRate)
	target := int(seconds * rate)
	if target < 0 {
		target = 0
	}

	p.sink.Lock()
	defer p.sink.Unlock()
	// Seeking exactly to the end would end the track, turning a scrub to the
	// right-hand edge into a skip. Stop one sample short.
	if n := p.src.streamer.Len(); target >= n {
		target = n - 1
	}
	if target < 0 {
		return
	}
	_ = p.src.streamer.Seek(target)
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
func (p *Player) playCurrent() {
	item := p.Current()
	if item == nil {
		p.stop()
		return
	}

	src, err := open(item.Path)

	p.mu.Lock()
	p.sink.Clear()
	p.src.Close()
	p.src, p.ctrl, p.vol = nil, nil, nil
	p.gen++
	gen := p.gen

	if err != nil {
		if p.failed == nil {
			p.failed = make(map[string]error)
		}
		p.failed[item.Path] = err
		p.lastErr = err
		p.mu.Unlock()
		p.changed()
		// A file that will not open is not a reason to stop the queue — it is a
		// reason to move past it. errored=true also suppresses repeat-one, so a
		// broken file cannot loop forever.
		p.advance(true)
		return
	}

	p.src = src
	delete(p.failed, item.Path) // it played this time
	streamer := beep.Streamer(src.streamer)
	if src.format.SampleRate != SampleRate {
		streamer = beep.Resample(resampleQuality, src.format.SampleRate, SampleRate, streamer)
	}
	p.ctrl = &beep.Ctrl{Streamer: streamer}
	p.vol = &effects.Volume{Streamer: p.ctrl, Base: 2, Volume: volumeToDB(p.volume), Silent: p.volume == 0}
	vol := p.vol
	p.mu.Unlock()

	p.sink.Play(beep.Seq(vol, beep.Callback(func() {
		// Runs on the sink's goroutine: hand off rather than doing work here,
		// where taking the sink lock would deadlock.
		select {
		case p.ended <- gen:
		default:
		}
	})))
	p.changed()
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
	p.sink.Clear()
	p.src.Close()
	p.src, p.ctrl, p.vol = nil, nil, nil
	p.gen++
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
