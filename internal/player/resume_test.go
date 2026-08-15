package player

import (
	"testing"

	"daemonlord.ygg/madplayer/internal/queue"
)

// A restored queue carries where the current track had got to, and
// docs/ui/player-and-queue.md is precise about what that means: the queue comes
// back PAUSED, pressing play resumes mid-track, and clicking a row starts that
// row from the beginning. All three are one rule — the position belongs to a
// named row, is consumed once, and is dropped by any explicit navigation.

// restored builds a player holding a two-track queue with an armed position, the
// way the UI does at startup.
func restored(t *testing.T, at float64) (*Player, *fakeSink, []*queue.Item) {
	t.Helper()
	dir := t.TempDir()
	// The durations are what a saved queue.json actually carries: the UI captures
	// them from the library when the queue is built, which is what lets a
	// restored track show its length before anything opens the file.
	items := []*queue.Item{
		{Path: writeWAV(t, dir, "a.wav", 5), Duration: 5},
		{Path: writeWAV(t, dir, "b.wav", 5), Duration: 5},
	}

	sink := &fakeSink{}
	p, err := New(sink)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })

	p.Restore(items, nil, 0, false, queue.RepeatOff)
	p.ResumeAt(at)
	return p, sink, items
}

// Restoring must not start anything. A player that began making noise by itself
// at launch would be a surprise, and a bad one at three in the morning.
func TestRestoringDoesNotStartPlaying(t *testing.T) {
	p, sink, items := restored(t, 2)
	if p.Playing() {
		t.Error("a restored queue started playing by itself")
	}
	if cur := p.Current(); cur == nil || cur.Path != items[0].Path {
		t.Errorf("current = %v, want the saved track", cur)
	}
	// Nothing was opened, which is the claim — and the sink is where that shows,
	// not the position: since 2026-08-15 the position ANSWERS for a track that
	// has not been decoded yet (see below), so a zero there would no longer mean
	// what this test is about.
	sink.mu.Lock()
	opened := sink.s != nil
	sink.mu.Unlock()
	if opened {
		t.Error("restoring opened the track")
	}
}

// The bar has something to show before a byte is read.
//
// A restored queue used to report 0:00 of 0:00 for a five-minute song, because
// the position came only from an open decoder — so a resumed session looked like
// an empty player until you pressed play. The queue item carries the length, the
// armed offset says where the track will start, and neither needs the file.
// Seekable stays false: there is a length to read and still nothing to scrub.
func TestARestoredTrackShowsItsLengthAndResumePointBeforeItOpens(t *testing.T) {
	p, _, _ := restored(t, 2)

	elapsed, total := p.Position()
	if elapsed != 2 {
		t.Errorf("elapsed = %.2f, want the armed resume point (2)", elapsed)
	}
	if total < 4.9 || total > 5.1 {
		t.Errorf("total = %.2f, want the queue item's duration (~5)", total)
	}
	if p.Seekable() {
		t.Error("a track nothing has opened reported itself scrubbable")
	}

	// The offset belongs to a named row. Moving on drops it, and the next track
	// shows its own length from zero rather than inheriting somebody else's mark.
	p.Next()
	if elapsed, _ := p.Position(); elapsed != 0 {
		t.Errorf("elapsed = %.2f after moving on, want 0", elapsed)
	}
}

// Pressing play is the gesture that says "carry on where I was".
func TestPressingPlayResumesMidTrack(t *testing.T) {
	p, _, _ := restored(t, 2)

	p.Toggle()
	waitPlaying(t, p)

	elapsed, _ := p.Position()
	if elapsed < 1.9 || elapsed > 2.3 {
		t.Errorf("resumed at %.2f, want ~2", elapsed)
	}
}

// Once used, the offset is gone: the NEXT track starts at its own beginning.
// A bare saved number with no owner is exactly what would seek it to 2s.
func TestTheResumeIsConsumedOnce(t *testing.T) {
	p, _, _ := restored(t, 2)

	p.Toggle()
	waitPlaying(t, p)
	p.Next()
	waitPlaying(t, p)

	if elapsed, _ := p.Position(); elapsed > 0.5 {
		t.Errorf("the next track started at %.2f — the resume outlived its track", elapsed)
	}
}

// Clicking a row starts that row from the beginning, even when it is the very
// row the saved position belongs to. Two different gestures, two answers.
func TestClickingTheRestoredRowStartsItFromTheBeginning(t *testing.T) {
	p, _, items := restored(t, 2)

	p.SetQueue(items, 0)
	waitPlaying(t, p)

	if elapsed, _ := p.Position(); elapsed > 0.5 {
		t.Errorf("a clicked row started at %.2f, want the beginning", elapsed)
	}
}

// A saved position past the end of the track is what a truncated file or an
// edited queue.json produces. Seeking there would end the track instantly and
// look like a skip.
func TestAResumePastTheEndIsIgnored(t *testing.T) {
	p, _, _ := restored(t, 9999)

	p.Toggle()
	waitPlaying(t, p)

	elapsed, total := p.Position()
	if elapsed > 0.5 {
		t.Errorf("resumed at %.2f of %.2f — an impossible position was honoured", elapsed, total)
	}
}

// Nothing to resume into is the ordinary first run.
func TestResumeWithAnEmptyQueueDoesNothing(t *testing.T) {
	sink := &fakeSink{}
	p, err := New(sink)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	p.ResumeAt(30) // must not panic, and must not arm anything
	if elapsed, _ := p.Position(); elapsed != 0 {
		t.Errorf("position = %.2f on an empty player", elapsed)
	}
}
