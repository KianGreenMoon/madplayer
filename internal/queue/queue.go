package queue

// Item is one entry in the queue.
//
// Path is the identity — the local analogue of the web UI's rowKey, which is
// `ts:<tagset_id>` for a library appearance and falls back to `url:<url>`. A
// local file has neither, and the path is what the decoder opens anyway.
type Item struct {
	Path     string  `json:"path"`
	Title    string  `json:"title"`
	Artist   string  `json:"artist"`
	Album    string  `json:"album"`
	Duration float64 `json:"duration,omitempty"`
}

// RowKey answers "is this the row I am looking at?" — it decides which row is
// highlighted and whether clicking it toggles pause or restarts.
func (i *Item) RowKey() string { return "path:" + i.Path }

// Repeat is the three-state repeat mode. It affects only what happens when a
// track ENDS; manual Next/Prev always wrap regardless.
type Repeat int

const (
	RepeatOff Repeat = iota
	RepeatAll
	RepeatOne
)

func (r Repeat) String() string {
	switch r {
	case RepeatAll:
		return "all"
	case RepeatOne:
		return "one"
	}
	return "off"
}

// Next cycles off → all → one → off.
func (r Repeat) Next() Repeat { return (r + 1) % 3 }

type snapshot struct {
	items    []*Item
	original []*Item
	index    int
	shuffled bool
}

// Queue owns the play order.
//
// Two orders exist while shuffled: items is what plays and what the panel shows,
// original is the order to restore. Shuffle is NOT "pick a random next track" —
// it transforms the queue, and Next/Prev simply walk the visible one.
type Queue struct {
	items    []*Item
	original []*Item
	index    int
	shuffled bool
	dirty    bool
	repeat   Repeat
	stash    *snapshot
	rnd      func() float64
}

// New returns an empty queue.
func New() *Queue { return &Queue{index: -1} }

// SetRand injects the shuffle's randomness, for tests.
func (q *Queue) SetRand(fn func() float64) { q.rnd = fn }

func (q *Queue) Items() []*Item     { return q.items }
func (q *Queue) Len() int           { return len(q.items) }
func (q *Queue) Index() int         { return q.index }
func (q *Queue) Shuffled() bool     { return q.shuffled }
func (q *Queue) Dirty() bool        { return q.dirty }
func (q *Queue) Repeat() Repeat     { return q.repeat }
func (q *Queue) CanUndo() bool      { return q.stash != nil }
func (q *Queue) SetRepeat(r Repeat) { q.repeat = r }

// Current is the playing item, or nil.
func (q *Queue) Current() *Item {
	if q.index < 0 || q.index >= len(q.items) {
		return nil
	}
	return q.items[q.index]
}

// Set replaces the queue — the view you clicked in becomes the queue, in the
// order shown. Browsing never changes the queue; only this or an explicit edit
// does.
//
// If the queue had been hand-edited, the previous one (including its
// un-shuffled original order) is stashed so the replacement can be undone.
// Returns true when an undo is available, which is the caller's cue to offer it.
func (q *Queue) Set(items []*Item, index int) bool {
	offered := false
	if q.dirty && len(q.items) > 0 {
		q.stash = &snapshot{items: q.items, original: q.original, index: q.index, shuffled: q.shuffled}
		offered = true
	} else {
		q.stash = nil
	}

	q.items = append([]*Item(nil), items...)
	q.index = ClampIndex(index, len(q.items))
	q.original = nil
	q.dirty = false

	// Setting a new queue while shuffle is on shuffles it immediately, with the
	// clicked track first — otherwise turning shuffle on would be a mode that
	// silently stops applying.
	if q.shuffled {
		q.applyShuffle()
	}
	return offered
}

// applyShuffle snapshots the current order and reorders behind the current item.
func (q *Queue) applyShuffle() {
	cur := q.Current()
	q.original = append([]*Item(nil), q.items...)
	perm := ShufflePerm(len(q.items), q.index, q.rnd)
	next := make([]*Item, 0, len(perm))
	for _, p := range perm {
		next = append(next, q.items[p])
	}
	q.items = next
	q.index = indexOf(q.items, cur)
}

// ToggleShuffle transforms the queue. Playback is never interrupted in either
// direction — only the order changes, and the current track stays current.
func (q *Queue) ToggleShuffle() {
	if len(q.items) == 0 {
		q.shuffled = !q.shuffled
		return
	}
	if q.shuffled {
		cur := q.Current()
		q.items = q.original
		q.original = nil
		q.shuffled = false
		q.index = indexOf(q.items, cur)
		return
	}
	q.shuffled = true
	q.applyShuffle()
}

// Next steps forward. Manual navigation ALWAYS wraps, whatever the repeat mode.
func (q *Queue) Next() bool {
	if len(q.items) == 0 {
		return false
	}
	q.index = (q.index + 1) % len(q.items)
	return true
}

// Prev steps back through the VISIBLE order.
//
// There is no play history: after a reshuffle this walks the shuffled order
// rather than what actually played. Known limitation, shared with the web UI on
// purpose — a history stack is a possible follow-up, and diverging here would
// make the two clients disagree.
func (q *Queue) Prev() bool {
	if len(q.items) == 0 {
		return false
	}
	q.index = (q.index - 1 + len(q.items)) % len(q.items)
	return true
}

// TrackEnded advances after a track finishes on its own, and reports whether
// something should now play. Unlike Next/Prev this obeys the repeat mode.
//
// errored suppresses repeat-one, so a broken file cannot loop forever.
func (q *Queue) TrackEnded(errored bool) bool {
	if len(q.items) == 0 {
		return false
	}
	if q.repeat == RepeatOne && !errored {
		return true // same index: replay
	}
	if q.index+1 < len(q.items) {
		q.index++
		return true
	}
	if q.repeat == RepeatAll {
		q.index = 0
		return true
	}
	return false // off: stop at the end of the queue
}

// PlayNext inserts right after the current track. Marks the queue dirty.
func (q *Queue) PlayNext(items ...*Item) {
	at := 0
	if q.index >= 0 {
		at = q.index + 1
	}
	q.insert(at, items)
}

// Append adds to the end. Marks the queue dirty.
func (q *Queue) Append(items ...*Item) { q.insert(len(q.items), items) }

func (q *Queue) insert(at int, items []*Item) {
	if len(items) == 0 {
		return
	}
	cur := q.Current()
	q.items = insertAt(q.items, at, items)
	q.index = InsertAdjust(q.index, at, len(items))

	// Mirror into the original order so turning shuffle off does not lose the
	// addition. An insert lands right after the current track's ORIGINAL
	// position — a shuffled position has no exact counterpart there.
	if q.shuffled {
		oat := len(q.original)
		if cur != nil {
			if oi := indexOf(q.original, cur); oi >= 0 {
				oat = oi + 1
			}
		}
		q.original = insertAt(q.original, oat, items)
	}
	q.dirty = true
}

// Remove drops position i and reports whether the removed item was the one
// playing — which is a playback decision the caller has to make, not the
// queue's.
func (q *Queue) Remove(i int) (wasCurrent bool) {
	if i < 0 || i >= len(q.items) {
		return false
	}
	it := q.items[i]
	wasCurrent = i == q.index

	q.items = append(q.items[:i:i], q.items[i+1:]...)
	if wasCurrent {
		q.index = ClampIndex(q.index, len(q.items))
	} else {
		q.index = RemoveAdjust(q.index, i)
	}
	if q.shuffled {
		if oi := indexOf(q.original, it); oi >= 0 {
			q.original = append(q.original[:oi:oi], q.original[oi+1:]...)
		}
	}
	q.dirty = true
	return wasCurrent
}

// Move reorders the visible queue.
//
// It deliberately does NOT touch the original order: a drag in the panel is an
// edit to the temporary play order, so turning shuffle off still restores the
// order the queue actually came in.
func (q *Queue) Move(from, to int) {
	n := len(q.items)
	if from < 0 || from >= n || to < 0 || to >= n || from == to {
		return
	}
	it := q.items[from]
	rest := append(q.items[:from:from], q.items[from+1:]...)
	q.items = insertAt(rest, to, []*Item{it})
	q.index = MoveAdjust(q.index, from, to)
	q.dirty = true
}

// Clear empties the queue.
func (q *Queue) Clear() {
	q.items, q.original, q.index, q.dirty = nil, nil, -1, true
}

// Undo restores the queue stashed by the last Set, including its un-shuffled
// original order.
func (q *Queue) Undo() bool {
	if q.stash == nil {
		return false
	}
	q.items, q.original = q.stash.items, q.stash.original
	q.index, q.shuffled = q.stash.index, q.stash.shuffled
	q.stash = nil
	q.dirty = true
	return true
}

// Restore revives a persisted queue. original is relinked to share pointers with
// items, without which un-shuffle and remove would silently stop working.
func (q *Queue) Restore(items, original []*Item, index int, shuffled bool, repeat Repeat) {
	q.items = items
	q.index = ClampIndex(index, len(items))
	q.shuffled = shuffled
	q.repeat = repeat
	q.dirty = false
	q.stash = nil
	if shuffled && len(original) > 0 {
		q.original = Relink(original, items)
	} else {
		q.original = nil
	}
}

// Original is the un-shuffled order, for persistence. Nil when not shuffled.
func (q *Queue) Original() []*Item { return q.original }

func indexOf(list []*Item, want *Item) int {
	if want == nil {
		return -1
	}
	for i, it := range list {
		if it == want {
			return i
		}
	}
	return -1
}

func insertAt(list []*Item, at int, items []*Item) []*Item {
	if at < 0 {
		at = 0
	}
	if at > len(list) {
		at = len(list)
	}
	out := make([]*Item, 0, len(list)+len(items))
	out = append(out, list[:at]...)
	out = append(out, items...)
	out = append(out, list[at:]...)
	return out
}
