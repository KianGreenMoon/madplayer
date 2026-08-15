package remote

// The rules that keep a GUESS from making a PERSON wait: the mesh slot is
// single and a streamed transfer can hold it for minutes, so who may hold it,
// for how long, and who gets thrown off it are load-bearing decisions.
// All three tests here were written against reproduced failures
// (.issues/open-issues.md, 2026-08-15).

import (
	"context"
	"io"
	"testing"
	"time"

	"daemonlord.ygg/madplayer/internal/queue"
)

// blockingReader stands in for a swarm transfer that dribbles forever: bytes
// are always about to arrive and never do.
type blockingReader struct{ unblock chan struct{} }

func (r *blockingReader) Read(p []byte) (int, error) {
	<-r.unblock
	return 0, io.EOF
}

const (
	hashP = "1111111111111111111111111111111111111111111111111111111111111111"
	hashC = "2222222222222222222222222222222222222222222222222222222222222222"
)

func waitForSwarmCalls(t *testing.T, sw *fakeSwarm, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		sw.mu.Lock()
		calls := sw.calls
		sw.mu.Unlock()
		if calls >= want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("swarm saw %d call(s), want %d", calls, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// The queue's next track is being prefetched over the swarm, mid-transfer. The
// person clicks a different track. The click must produce music within the
// swarm budget plus relay time — before the fix it queued on the mutex behind
// the prefetch's whole transfer, with a spinner and no bound (reproduced:
// a 2s deadline expired with zero bytes every run).
func TestAClickedTrackIsNotStarvedByAPrefetchMidTransfer(t *testing.T) {
	ms := newMeshServer(t, "the clicked track's audio", "aa11")
	slow := &blockingReader{unblock: make(chan struct{})}
	t.Cleanup(func() { close(slow.unblock) })
	sw := &fakeSwarm{stream: slow}
	f := meshFetcher(t, ms, sw, &fakeVouch{ok: true})
	f.SetSwarmBudget(150 * time.Millisecond)

	f.Prefetch(ms.track(hashP))
	waitForSwarmCalls(t, sw, 1)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	path, err := f.Local(ctx, ms.track(hashC))
	if err != nil {
		t.Fatalf("the clicked track produced no bytes: %v", err)
	}
	if got := read(t, path); got != "the clicked track's audio" {
		t.Fatalf("clicked track played %q", got)
	}
}

// A real fetch for a different track THROWS THE PREFETCH OFF the slot, rather
// than waiting for it: a guess about the future must never outrank a click.
func TestARealFetchPreemptsARunningPrefetch(t *testing.T) {
	ms := newMeshServer(t, "audio", "aa11")
	slow := &blockingReader{unblock: make(chan struct{})}
	t.Cleanup(func() { close(slow.unblock) })
	sw := &fakeSwarm{stream: slow}
	f := meshFetcher(t, ms, sw, &fakeVouch{ok: true})
	f.SetSwarmBudget(150 * time.Millisecond)

	f.Prefetch(ms.track(hashP))
	waitForSwarmCalls(t, sw, 1)

	if _, err := f.Local(context.Background(), ms.track(hashC)); err != nil {
		t.Fatalf("clicked track: %v", err)
	}
	// The preempted prefetch lets go of the swarm: its reader is cancelled, so
	// the fake's live count drains rather than blocking forever.
	deadline := time.Now().Add(2 * time.Second)
	for sw.live.Load() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("the preempted prefetch is still holding its swarm fetch")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Prefetch(next) means "THE guess is now next": whatever else was being
// prefetched is stale and stops, even when the new guess is already on disk.
// The cached-early-return used to come first and left the stale download
// running — which is exactly the shape the starvation repro needed.
func TestANewPrefetchStopsTheStaleOneEvenWhenCached(t *testing.T) {
	ms := newMeshServer(t, "cached audio", "aa11")
	slow := &blockingReader{unblock: make(chan struct{})}
	t.Cleanup(func() { close(slow.unblock) })
	sw := &fakeSwarm{stream: slow}
	f := meshFetcher(t, ms, sw, &fakeVouch{ok: true})
	f.SetSwarmBudget(150 * time.Millisecond)

	// Cache a track over the relay first (no hash → the swarm is skipped).
	cached := &queue.Item{URL: ms.URL + "/files/plain/song.flac", Origin: "home"}
	if _, err := f.Local(context.Background(), cached); err != nil {
		t.Fatalf("caching the relay track: %v", err)
	}

	f.Prefetch(ms.track(hashP)) // the stale guess, stuck mid-transfer
	waitForSwarmCalls(t, sw, 1)
	f.Prefetch(cached) // the queue moved; the new guess is already here

	deadline := time.Now().Add(2 * time.Second)
	for sw.live.Load() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("the stale prefetch kept downloading after the guess changed")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// The slot wait, the holder lookup and the first byte share ONE budget. Each
// stage getting its own multiplied the worst-case silence by the number of
// stages — the comment always claimed sharing, the code gave the first byte a
// fresh allowance after the holder lookup had spent freely.
func TestTheHolderLookupAndTheFirstByteShareOneBudget(t *testing.T) {
	ms := newMeshServer(t, "audio", "aa11")
	ms.holdersDelay = 300 * time.Millisecond
	sw := &fakeSwarm{body: "audio"}
	f := meshFetcher(t, ms, sw, &fakeVouch{ok: true})
	f.SetSwarmBudget(time.Second)

	if _, err := f.Local(context.Background(), ms.track(hashP)); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	got := time.Duration(sw.lastFirstByte.Load())
	if got <= 0 {
		t.Fatal("the swarm was never reached")
	}
	// The lookup spent ~300ms of the 1s budget, so the first-byte allowance
	// must arrive visibly smaller — and never a fresh full budget.
	if got > 750*time.Millisecond {
		t.Fatalf("first-byte allowance = %s after a 300ms holder lookup; the stages are not sharing the budget", got)
	}
}
