// Package queue is the play queue: the index arithmetic and the shuffle/repeat
// semantics defined in docs/ui/player-and-queue.md.
//
// That doc is a CROSS-CLIENT CONTRACT, and it names this as the one piece still
// decided client-side — `webui/static/js/queue-ops.js` is the reference
// implementation, and this file is its Go twin, function for function, with the
// same test cases. Porting the rules is fine; inventing different ones is not:
// two clients that disagree about what shuffle does cannot share a queue.
package queue

import "math/rand"

// InsertAdjust returns the current-track index after inserting count tracks at
// position at. A non-playing index (-1) never moves.
func InsertAdjust(index, at, count int) int {
	if index < 0 {
		return index
	}
	if at <= index {
		return index + count
	}
	return index
}

// RemoveAdjust returns the current-track index after removing position i, for
// the cases where the removed track is NOT the current one. Removing the current
// track is a playback decision — the caller picks what plays next.
func RemoveAdjust(index, i int) int {
	if index < 0 {
		return index
	}
	if i < index {
		return index - 1
	}
	return index
}

// MoveAdjust returns the current-track index after moving position from to
// position to (remove-then-insert semantics).
func MoveAdjust(index, from, to int) int {
	if index < 0 {
		return index
	}
	switch {
	case from == index:
		return to
	case from < index && to >= index:
		return index - 1
	case from > index && to <= index:
		return index + 1
	}
	return index
}

// ClampIndex confines a restored or requested index to the queue's bounds, or
// -1 for an empty queue.
func ClampIndex(i, length int) int {
	if length <= 0 {
		return -1
	}
	if i < 0 {
		return 0
	}
	if i > length-1 {
		return length - 1
	}
	return i
}

// ShufflePerm returns a play-order permutation of [0..n): the current position
// (if any) first, the rest Fisher-Yates shuffled. rnd is injectable so the
// shuffle is reproducible in tests; pass nil for the global source.
func ShufflePerm(n, current int, rnd func() float64) []int {
	if rnd == nil {
		rnd = rand.Float64
	}
	rest := make([]int, 0, n)
	for i := 0; i < n; i++ {
		if i != current {
			rest = append(rest, i)
		}
	}
	for i := len(rest) - 1; i > 0; i-- {
		j := int(rnd() * float64(i+1))
		if j > i {
			j = i // guard a rnd() that returns exactly 1
		}
		rest[i], rest[j] = rest[j], rest[i]
	}
	if current >= 0 && current < n {
		return append([]int{current}, rest...)
	}
	return rest
}

// Relink rebuilds the original-order slice so it SHARES pointers with the
// (shuffled) queue after both were revived from JSON. Identity-based operations
// — un-shuffle, remove — depend on shared references, and a round trip through
// JSON gives every item a fresh address. Duplicates are matched by occurrence;
// an entry with no match is kept as-is.
func Relink(original, queue []*Item) []*Item {
	pool := make(map[string][]*Item, len(queue))
	for _, t := range queue {
		pool[t.Path] = append(pool[t.Path], t)
	}
	out := make([]*Item, len(original))
	for i, o := range original {
		if c := pool[o.Path]; len(c) > 0 {
			out[i] = c[0]
			pool[o.Path] = c[1:]
			continue
		}
		out[i] = o
	}
	return out
}
