package queue

import (
	"reflect"
	"sort"
	"testing"
)

// The first six tests mirror tests/js/queue-ops.test.mjs case for case. If one
// of these ever needs different expectations from its JS twin, the cross-client
// contract has changed and docs/ui/player-and-queue.md moves in the same commit.

func TestInsertAdjust(t *testing.T) {
	cases := []struct {
		index, at, count, want int
		why                    string
	}{
		{-1, 0, 3, -1, "no current track: unchanged"},
		{2, 0, 2, 4, "insert before current shifts it"},
		{2, 2, 1, 3, "insert at current shifts it (track keeps playing)"},
		{2, 3, 5, 2, "insert after current: unchanged"},
	}
	for _, c := range cases {
		if got := InsertAdjust(c.index, c.at, c.count); got != c.want {
			t.Errorf("InsertAdjust(%d,%d,%d) = %d, want %d — %s", c.index, c.at, c.count, got, c.want, c.why)
		}
	}
}

func TestRemoveAdjust(t *testing.T) {
	cases := []struct {
		index, i, want int
		why            string
	}{
		{-1, 0, -1, "no current track: unchanged"},
		{3, 1, 2, "remove before current shifts it down"},
		{3, 4, 3, "remove after current: unchanged"},
	}
	for _, c := range cases {
		if got := RemoveAdjust(c.index, c.i); got != c.want {
			t.Errorf("RemoveAdjust(%d,%d) = %d, want %d — %s", c.index, c.i, got, c.want, c.why)
		}
	}
}

func TestMoveAdjust(t *testing.T) {
	cases := []struct {
		index, from, to, want int
		why                   string
	}{
		{-1, 0, 2, -1, "no current track: unchanged"},
		{2, 2, 5, 5, "moving the current track follows it"},
		{2, 5, 0, 3, "moving a later track before current shifts current up"},
		{2, 0, 5, 1, "moving an earlier track after current shifts current down"},
		{2, 0, 1, 2, "reorder entirely before current: unchanged"},
		{2, 4, 5, 2, "reorder entirely after current: unchanged"},
		{2, 0, 2, 1, "move earlier track TO the current slot shifts current down"},
		{2, 4, 2, 3, "move later track TO the current slot shifts current up"},
	}
	for _, c := range cases {
		if got := MoveAdjust(c.index, c.from, c.to); got != c.want {
			t.Errorf("MoveAdjust(%d,%d,%d) = %d, want %d — %s", c.index, c.from, c.to, got, c.want, c.why)
		}
	}
}

func TestClampIndex(t *testing.T) {
	cases := []struct {
		i, length, want int
		why             string
	}{
		{5, 0, -1, "empty queue: -1"},
		{-3, 4, 0, "negative clamps to 0"},
		{9, 4, 3, "past the end clamps to last"},
		{2, 4, 2, "in range: unchanged"},
	}
	for _, c := range cases {
		if got := ClampIndex(c.i, c.length); got != c.want {
			t.Errorf("ClampIndex(%d,%d) = %d, want %d — %s", c.i, c.length, got, c.want, c.why)
		}
	}
}

func TestShufflePerm(t *testing.T) {
	perm := ShufflePerm(5, 2, nil)
	if perm[0] != 2 {
		t.Errorf("perm[0] = %d, want the current position first", perm[0])
	}
	sorted := append([]int(nil), perm...)
	sort.Ints(sorted)
	if !reflect.DeepEqual(sorted, []int{0, 1, 2, 3, 4}) {
		t.Errorf("not a permutation of [0..n): %v", perm)
	}

	sorted = ShufflePerm(4, -1, nil)
	sort.Ints(sorted)
	if !reflect.DeepEqual(sorted, []int{0, 1, 2, 3}) {
		t.Errorf("no current track: want a full permutation, got %v", sorted)
	}

	// rnd()=0 always swaps with slot 0 — the same determinism the JS test uses.
	zero := func() float64 { return 0 }
	if got := ShufflePerm(4, 1, zero); !reflect.DeepEqual(got, []int{1, 2, 3, 0}) {
		t.Errorf("injected rand: got %v, want [1 2 3 0]", got)
	}
	if got := ShufflePerm(1, 0, nil); !reflect.DeepEqual(got, []int{0}) {
		t.Errorf("single track: got %v, want [0]", got)
	}
	if got := ShufflePerm(0, -1, nil); len(got) != 0 {
		t.Errorf("empty queue: got %v, want []", got)
	}
}

func TestRelink(t *testing.T) {
	a := &Item{Path: "/m/x.mp3"}
	b1 := &Item{Path: "/m/y.mp3"}
	b2 := &Item{Path: "/m/y.mp3"} // duplicate of the same track
	q := []*Item{b1, a, b2}

	// Revived from JSON: equal data, different pointers.
	orig := []*Item{{Path: "/m/x.mp3"}, {Path: "/m/y.mp3"}, {Path: "/m/y.mp3"}, {Path: "/m/gone.mp3"}}
	got := Relink(orig, q)

	if got[0] != a {
		t.Error("matched by path → should share the queue's pointer")
	}
	if got[1] != b1 || got[2] != b2 {
		t.Error("duplicates should consume occurrences in order")
	}
	if got[3] != orig[3] {
		t.Error("unmatched entry should be kept as-is")
	}
}

// --- the semantic layer -----------------------------------------------------

func items(names ...string) []*Item {
	out := make([]*Item, len(names))
	for i, n := range names {
		out[i] = &Item{Path: "/m/" + n, Title: n}
	}
	return out
}

func titles(list []*Item) []string {
	out := make([]string, len(list))
	for i, it := range list {
		out[i] = it.Title
	}
	return out
}

func TestSetReplacesAndOffersUndoOnlyWhenDirty(t *testing.T) {
	q := New()
	q.Set(items("a", "b", "c"), 0)
	if q.Set(items("d", "e"), 0) {
		t.Error("a clean queue should not offer an undo")
	}

	q.Set(items("a", "b", "c"), 1)
	q.Append(items("z")...) // a hand edit
	if !q.Dirty() {
		t.Fatal("Append should mark the queue dirty")
	}
	if !q.Set(items("d", "e"), 0) {
		t.Error("replacing a hand-edited queue should offer an undo")
	}
	if !q.Undo() || !reflect.DeepEqual(titles(q.Items()), []string{"a", "b", "c", "z"}) {
		t.Errorf("undo restored %v", titles(q.Items()))
	}
}

func TestShuffledQueuePinsCurrentFirstAndRestoresOnOff(t *testing.T) {
	q := New()
	q.SetRand(func() float64 { return 0 })
	q.Set(items("a", "b", "c", "d"), 2) // current = "c"

	q.ToggleShuffle()
	if q.Items()[0].Title != "c" {
		t.Errorf("shuffled order = %v, want the current track pinned first", titles(q.Items()))
	}
	if q.Index() != 0 {
		t.Errorf("index = %d, want 0 (the current track moved to the front)", q.Index())
	}

	q.ToggleShuffle()
	if got := titles(q.Items()); !reflect.DeepEqual(got, []string{"a", "b", "c", "d"}) {
		t.Errorf("un-shuffled order = %v, want the original", got)
	}
	if q.Current().Title != "c" {
		t.Errorf("current = %q, want c — playback must never be interrupted", q.Current().Title)
	}
}

func TestSettingAQueueWhileShuffledShufflesItImmediately(t *testing.T) {
	q := New()
	q.SetRand(func() float64 { return 0 })
	q.Set(items("a", "b"), 0)
	q.ToggleShuffle()

	q.Set(items("p", "q", "r"), 2) // clicked "r"
	if q.Items()[0].Title != "r" {
		t.Errorf("new shuffled queue = %v, want the clicked track first", titles(q.Items()))
	}
	if !q.Shuffled() {
		t.Error("shuffle turned itself off on a new queue")
	}
}

func TestInsertWhileShuffledMirrorsIntoTheOriginalOrder(t *testing.T) {
	q := New()
	q.SetRand(func() float64 { return 0 })
	q.Set(items("a", "b", "c"), 0) // current = "a"
	q.ToggleShuffle()

	q.PlayNext(items("new")...)
	q.ToggleShuffle() // back to the original order

	got := titles(q.Items())
	want := []string{"a", "new", "b", "c"} // right after the current track's original position
	if !reflect.DeepEqual(got, want) {
		t.Errorf("original order after a shuffled insert = %v, want %v", got, want)
	}
}

func TestRemoveWhileShuffledMirrorsIntoTheOriginalOrder(t *testing.T) {
	q := New()
	q.SetRand(func() float64 { return 0 })
	q.Set(items("a", "b", "c"), 0)
	q.ToggleShuffle()

	// Remove whichever item is last in the shuffled view.
	last := len(q.Items()) - 1
	gone := q.Items()[last].Title
	q.Remove(last)
	q.ToggleShuffle()

	for _, tt := range titles(q.Items()) {
		if tt == gone {
			t.Errorf("%q survived in the original order after being removed", gone)
		}
	}
}

func TestMoveDoesNotTouchTheOriginalOrder(t *testing.T) {
	q := New()
	q.SetRand(func() float64 { return 0 })
	q.Set(items("a", "b", "c"), 0)
	q.ToggleShuffle()

	q.Move(1, 2) // a panel drag: temporary order only
	q.ToggleShuffle()

	if got := titles(q.Items()); !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Errorf("original order = %v, want it untouched by a panel drag", got)
	}
}

func TestRepeatAffectsOnlyTrackEndNotManualNavigation(t *testing.T) {
	q := New()
	q.Set(items("a", "b"), 1) // sitting on the last track

	// Off: a track ending at the end of the queue stops.
	if q.TrackEnded(false) {
		t.Error("repeat=off should stop at the end of the queue")
	}
	// ...but manual Next always wraps.
	q.Next()
	if q.Current().Title != "a" {
		t.Errorf("manual Next = %q, want it to wrap to a", q.Current().Title)
	}

	q.Set(items("a", "b"), 1)
	q.SetRepeat(RepeatAll)
	if !q.TrackEnded(false) || q.Current().Title != "a" {
		t.Error("repeat=all should wrap from the last track to the first")
	}

	q.Set(items("a", "b"), 0)
	q.SetRepeat(RepeatOne)
	if !q.TrackEnded(false) || q.Current().Title != "a" {
		t.Error("repeat=one should replay the same track")
	}
	// A broken file must not loop forever.
	if !q.TrackEnded(true) || q.Current().Title != "b" {
		t.Error("repeat=one must be suppressed when the track errored")
	}
}

func TestRemovingTheCurrentTrackIsReportedToTheCaller(t *testing.T) {
	q := New()
	q.Set(items("a", "b", "c"), 1)
	if !q.Remove(1) {
		t.Error("removing the current track should report it")
	}
	if q.Remove(0) {
		t.Error("removing a non-current track should not report it")
	}
}

func TestRestoreRelinksSoUnshuffleStillWorks(t *testing.T) {
	// Simulate a JSON round trip: same data, different pointers.
	visible := items("c", "a", "b")
	original := items("a", "b", "c")

	q := New()
	q.Restore(visible, original, 0, true, RepeatOff)
	q.ToggleShuffle()

	if got := titles(q.Items()); !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Errorf("restored un-shuffle = %v, want the original order", got)
	}
	if q.Current().Title != "c" {
		t.Errorf("current = %q, want c to stay current across the un-shuffle", q.Current().Title)
	}
}
