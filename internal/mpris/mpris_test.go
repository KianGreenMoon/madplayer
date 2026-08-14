package mpris

import (
	"math"
	"testing"

	"github.com/godbus/dbus/v5"

	"daemonlord.ygg/madplayer/internal/queue"
)

// The bus half of this package needs a session bus and is exercised by running
// the program (`playerctl -p madplayer …`). What is tested here is the half that
// has no bus in it: the translation between this player's vocabulary and MPRIS's,
// which is where the mistakes that make a media widget show nothing live.

// fake is Controls with no player behind it.
type fake struct {
	playing, paused, loading bool
	elapsed, total           float64
	volume                   float64
	shuffled                 bool
	repeat                   queue.Repeat
	cur                      *queue.Item
	qlen, qindex             int

	artPath  string
	seekedTo float64
	calls    []string
}

func (f *fake) Play()                        { f.calls = append(f.calls, "play") }
func (f *fake) Pause()                       { f.calls = append(f.calls, "pause") }
func (f *fake) Toggle()                      { f.calls = append(f.calls, "toggle") }
func (f *fake) Stop()                        { f.calls = append(f.calls, "stop") }
func (f *fake) Next()                        { f.calls = append(f.calls, "next") }
func (f *fake) Prev()                        { f.calls = append(f.calls, "prev") }
func (f *fake) Playing() bool                { return f.playing }
func (f *fake) Paused() bool                 { return f.paused }
func (f *fake) Loading() bool                { return f.loading }
func (f *fake) Position() (float64, float64) { return f.elapsed, f.total }
func (f *fake) Seek(s float64)               { f.seekedTo = s }
func (f *fake) Volume() float64              { return f.volume }
func (f *fake) SetVolume(v float64)          { f.volume = v }
func (f *fake) Shuffled() bool               { return f.shuffled }
func (f *fake) SetShuffle(on bool)           { f.shuffled = on }
func (f *fake) Repeat() queue.Repeat         { return f.repeat }
func (f *fake) SetRepeat(r queue.Repeat)     { f.repeat = r }
func (f *fake) Current() *queue.Item         { return f.cur }
func (f *fake) QueueLen() int                { return f.qlen }
func (f *fake) QueueIndex() int              { return f.qindex }
func (f *fake) ArtPath() string              { return f.artPath }

func svc(f *fake) *Service { return &Service{c: f} }

// Playing, Paused and Stopped are three states, and the two that are easy to
// confuse are the ones nothing on screen distinguishes.
func TestPlaybackStatus(t *testing.T) {
	item := &queue.Item{Title: "x", Path: "/x.flac"}
	for _, tc := range []struct {
		name string
		f    fake
		want string
	}{
		{"playing", fake{playing: true, cur: item}, "Playing"},
		{"downloading counts as playing", fake{loading: true, cur: item}, "Playing"},
		{"paused", fake{paused: true, cur: item}, "Paused"},
		{"stopped with a queue", fake{cur: item}, "Stopped"},
		{"nothing at all", fake{}, "Stopped"},
	} {
		f := tc.f
		if got := svc(&f).status(); got != tc.want {
			t.Errorf("%s: status = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// xesam:artist is a LIST. A bare string there is the single most common MPRIS
// mistake and it makes clients show no artist at all.
func TestArtistIsAList(t *testing.T) {
	f := &fake{cur: &queue.Item{Title: "Song", Artist: "Somebody", Album: "Record", Duration: 90}}
	m := svc(f).metadata()

	v, ok := m["xesam:artist"]
	if !ok {
		t.Fatal("no xesam:artist in the metadata")
	}
	if _, isList := v.Value().([]string); !isList {
		t.Fatalf("xesam:artist is %T, want []string", v.Value())
	}
	if got := m["xesam:title"].Value(); got != "Song" {
		t.Errorf("title = %v", got)
	}
	if got := m["mpris:length"].Value(); got != int64(90e6) {
		t.Errorf("length = %v, want microseconds", got)
	}
}

// An absent field must be absent, not present and empty: a client renders
// "Unknown" for a missing album and an empty line for an empty one.
func TestEmptyFieldsAreOmitted(t *testing.T) {
	f := &fake{cur: &queue.Item{Title: "Just a title", Path: "/x.flac"}}
	m := svc(f).metadata()
	for _, key := range []string{"xesam:artist", "xesam:album", "mpris:length"} {
		if _, present := m[key]; present {
			t.Errorf("%s is present for a track that has none", key)
		}
	}
	if _, ok := m["mpris:trackid"]; !ok {
		t.Error("no track id — clients key everything on it")
	}
}

// Nothing playing must still answer with a well-formed empty map; a nil one has
// no signature and clients reject it.
func TestNothingPlayingIsAnEmptyMap(t *testing.T) {
	m := svc(&fake{}).metadata()
	if m == nil {
		t.Fatal("metadata is nil with nothing playing")
	}
	if len(m) != 0 {
		t.Errorf("metadata is %v with nothing playing", m)
	}
}

// The id must change when the track does and hold while it does not: a client
// that sees it change knows to redraw, and one that sees it hold knows a
// metadata edit is about the same song.
func TestTheTrackIDFollowsTheTrack(t *testing.T) {
	one := &queue.Item{Title: "one", Path: "/one.flac"}
	two := &queue.Item{Title: "two", Path: "/two.flac"}
	s := svc(&fake{cur: one})

	first := s.trackID(one)
	if again := s.trackID(one); again != first {
		t.Errorf("id changed for the same track: %s then %s", first, again)
	}
	next := s.trackID(two)
	if next == first {
		t.Error("id did not change when the track did")
	}
	if !next.IsValid() {
		t.Errorf("%q is not a valid object path", next)
	}
}

// mpris:artUrl is a URL, so a cover with a space in its path has to survive
// being one — music directories are full of spaces and "#".
func TestArtUrlIsAFileURL(t *testing.T) {
	f := &fake{
		cur:     &queue.Item{Title: "x", Path: "/x.flac"},
		artPath: "/home/somebody/Music/Some Album #2/cover.png",
	}
	got, ok := svc(f).metadata()["mpris:artUrl"]
	if !ok {
		t.Fatal("no mpris:artUrl for a track whose cover is on disk")
	}
	want := "file:///home/somebody/Music/Some%20Album%20%232/cover.png"
	if got.Value() != want {
		t.Errorf("artUrl = %v, want %s", got.Value(), want)
	}

	// A track with no cover must carry no key at all: a client renders an empty
	// artUrl as a broken image rather than as no image.
	f.artPath = ""
	if _, present := svc(f).metadata()["mpris:artUrl"]; present {
		t.Error("mpris:artUrl is present for a track with no cover")
	}
}

// The three repeat modes are the same three under different words, and the
// mapping has to survive a round trip or the desktop and the window disagree.
func TestLoopStatusRoundTrips(t *testing.T) {
	for _, r := range []queue.Repeat{queue.RepeatOff, queue.RepeatAll, queue.RepeatOne} {
		name := loopName(r)
		back, ok := loopValue(name)
		if !ok || back != r {
			t.Errorf("%v -> %q -> %v (ok=%v)", r, name, back, ok)
		}
	}
	if _, ok := loopValue("Sideways"); ok {
		t.Error("an unknown LoopStatus was accepted")
	}
}

// A nonsense length from a broken decoder must not come out the other side as a
// negative one — clients render that as a progress bar running backwards.
func TestMicrosSaturates(t *testing.T) {
	for _, tc := range []struct {
		in   float64
		want int64
	}{
		{0, 0},
		{1.5, 1500000},
		{-3, 0},
		{math.NaN(), 0},
		{math.Inf(1), math.MaxInt64},
	} {
		if got := micros(tc.in); got != tc.want {
			t.Errorf("micros(%v) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// A widget can send a position for the track that was showing a moment ago.
// Honouring it would seek the WRONG song.
func TestSetPositionIgnoresAStaleTrackID(t *testing.T) {
	item := &queue.Item{Title: "current", Path: "/c.flac"}
	f := &fake{cur: item, total: 300}
	s := svc(f)
	h := playerHandler{s}

	if err := h.SetPosition(dbus.ObjectPath(objPath+"/Track/999"), 42e6); err != nil {
		t.Fatalf("SetPosition returned %v", err)
	}
	if f.seekedTo != 0 {
		t.Errorf("seeked to %v on a stale track id", f.seekedTo)
	}
}

// Relative seeks are what a scroll wheel over a media widget sends.
func TestSeekIsRelativeAndClampedAtZero(t *testing.T) {
	f := &fake{cur: &queue.Item{Title: "x", Path: "/x.flac"}, elapsed: 10, total: 300}
	s := svc(f)
	h := playerHandler{s}

	// No connection here: the seek must still reach the player, and the Seeked
	// signal is simply not sent. Reaching for a nil connection is what the first
	// version of this did, and it panicked.
	h.SeekBy(5e6)
	if f.seekedTo != 15 {
		t.Errorf("seeked to %v, want 15 (10 + 5)", f.seekedTo)
	}
	h.SeekBy(-60e6)
	if f.seekedTo != 0 {
		t.Errorf("seeked to %v, want a clamp at 0", f.seekedTo)
	}
}

// The Go method cannot be called Seek (vet reads it as a botched io.Seeker), so
// the bus name is restored by a rename — in BOTH the export and the
// introspection, or clients advertise a method nobody can invoke.
func TestTheIntrospectedNameIsTheBusName(t *testing.T) {
	names := map[string]bool{}
	for _, m := range playerMethods() {
		names[m.Name] = true
	}
	for _, want := range []string{"Next", "Previous", "Play", "Pause", "PlayPause", "Stop", "Seek", "SetPosition", "OpenUri"} {
		if !names[want] {
			t.Errorf("%s is missing from the introspection data", want)
		}
	}
	if names["SeekBy"] {
		t.Error("the Go method name leaked onto the bus")
	}
}

// A nil Service is what a machine with no session bus has, and every method on
// it has to be safe — the caller's whole error handling is one log line.
func TestANilServiceIsUsable(t *testing.T) {
	var s *Service
	s.Update()
	s.Tick()
	if err := s.Close(); err != nil {
		t.Errorf("closing a nil service returned %v", err)
	}
}
