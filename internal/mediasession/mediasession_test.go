package mediasession

import (
	"bytes"
	"testing"

	"daemonlord.ygg/madplayer/internal/queue"
)

// fakeControls is the player as a table of answers.
type fakeControls struct {
	cur     *queue.Item
	playing bool
	paused  bool
	loading bool
	elapsed float64
	total   float64
	art     string
}

func (f *fakeControls) Play()                        {}
func (f *fakeControls) Pause()                       {}
func (f *fakeControls) Stop()                        {}
func (f *fakeControls) Next()                        {}
func (f *fakeControls) Prev()                        {}
func (f *fakeControls) Playing() bool                { return f.playing }
func (f *fakeControls) Paused() bool                 { return f.paused }
func (f *fakeControls) Loading() bool                { return f.loading }
func (f *fakeControls) Position() (float64, float64) { return f.elapsed, f.total }
func (f *fakeControls) Seek(float64)                 {}
func (f *fakeControls) Current() *queue.Item         { return f.cur }
func (f *fakeControls) ArtPath() string              { return f.art }

var _ Controls = (*fakeControls)(nil)

func TestObserveNothingPlaying(t *testing.T) {
	if snap := observe(&fakeControls{}); snap.active {
		t.Fatalf("no current track must observe as inactive, got %+v", snap)
	}
	// A current item that is neither playing nor paused is STOPPED — the state
	// the bus's Stop leaves behind — and stopped means the service goes away.
	stopped := &fakeControls{cur: &queue.Item{Title: "left behind"}}
	if snap := observe(stopped); snap.active {
		t.Fatalf("a stopped track must observe as inactive, got %+v", snap)
	}
}

func TestObservePlaying(t *testing.T) {
	c := &fakeControls{
		cur:     &queue.Item{Title: "Endure Emptiness", Artist: "Kain Vinosec", Album: "A"},
		playing: true,
		elapsed: 12.5,
		total:   200,
		art:     "/covers/x.jpg",
	}
	snap := observe(c)
	if !snap.active || !snap.playing {
		t.Fatalf("playing track must be active+playing, got %+v", snap)
	}
	if snap.title != "Endure Emptiness" || snap.artist != "Kain Vinosec" || snap.album != "A" || snap.artPath != "/covers/x.jpg" {
		t.Fatalf("metadata not carried: %+v", snap)
	}
	if snap.positionMs != 12500 || snap.durationMs != 200000 {
		t.Fatalf("position/duration wrong: %+v", snap)
	}
}

func TestObserveLoadingCountsAsPlaying(t *testing.T) {
	// The person pressed play and the download is filling; a lock screen
	// saying "paused" would look like the command was lost. Same rule as the
	// MPRIS PlaybackStatus.
	c := &fakeControls{cur: &queue.Item{Title: "t"}, loading: true}
	snap := observe(c)
	if !snap.active || !snap.playing {
		t.Fatalf("loading must observe as playing, got %+v", snap)
	}
}

func TestObservePausedAndDurationFallback(t *testing.T) {
	// A paused track keeps its notification; and with no decoder total yet
	// (streaming), the queue item's library duration is the fallback.
	c := &fakeControls{cur: &queue.Item{Title: "t", Duration: 90}, paused: true}
	snap := observe(c)
	if !snap.active || snap.playing {
		t.Fatalf("paused must observe active+not playing, got %+v", snap)
	}
	if snap.durationMs != 90000 {
		t.Fatalf("duration fallback to the queue item's, got %+v", snap)
	}
}

func TestSameTrackIgnoresMotion(t *testing.T) {
	a := snapshot{active: true, title: "t", artist: "a", positionMs: 1000, playing: true}
	b := a
	b.positionMs = 2000
	b.playing = false
	if !a.sameTrack(b) {
		t.Fatal("position and play state must not force a metadata push")
	}
	b.artPath = "/new-cover.jpg"
	if a.sameTrack(b) {
		t.Fatal("a cover arriving later must force a metadata push")
	}
}

func TestModUTF8(t *testing.T) {
	cases := []struct {
		in   string
		want []byte
	}{
		// ASCII passes through, with the terminator.
		{"abc", []byte{'a', 'b', 'c', 0}},
		// Two- and three-byte sequences agree with real UTF-8.
		{"é", []byte{0xC3, 0xA9, 0}},
		{"€", []byte{0xE2, 0x82, 0xAC, 0}},
		// An embedded NUL is 0xC0 0x80, never a bare zero that would
		// truncate the Java string.
		{"a\x00b", []byte{'a', 0xC0, 0x80, 'b', 0}},
		// The one that aborts the process when it is wrong: a character
		// beyond the BMP is a CESU-8 surrogate pair, six bytes, not Go's
		// four-byte UTF-8. U+1F3B5 → D83C DFB5.
		{"🎵", []byte{0xED, 0xA0, 0xBC, 0xED, 0xBE, 0xB5, 0}},
	}
	for _, c := range cases {
		if got := modUTF8(c.in); !bytes.Equal(got, c.want) {
			t.Errorf("modUTF8(%q) = % X, want % X", c.in, got, c.want)
		}
	}
}
