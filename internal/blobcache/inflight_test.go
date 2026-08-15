package blobcache

import (
	"context"
	"io"
	"testing"
	"time"
)

// Playing an album: track 1 plays, track 2 is prefetched, track 1 ends, and the
// player asks for track 2 — which is ALREADY BEING FETCHED. If that second
// caller waits for the whole download instead of joining the stream, every track
// after the first waits out its own download and streaming buys nothing.
func TestASecondCallerJoinsTheStreamInsteadOfWaiting(t *testing.T) {
	c := cache(t)
	payload := make([]byte, 64<<10)

	// The prefetch.
	first, err := c.Stream(context.Background(), "k", ".mp3", trickleFetch(payload, 4<<10, 25*time.Millisecond, nil))
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if _, err := io.ReadFull(first, make([]byte, 4<<10)); err != nil {
		t.Fatal(err)
	}

	// The player, arriving while that is still running.
	start := time.Now()
	second, err := c.Stream(context.Background(), "k", ".mp3", trickleFetch(payload, 4<<10, 25*time.Millisecond, nil))
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if _, err := io.ReadFull(second, make([]byte, 4<<10)); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)

	// 16 pieces at 25ms is ~400ms for the whole thing.
	if elapsed > 200*time.Millisecond {
		t.Errorf("the second caller waited %v for its first bytes — it waited out the download instead of joining the stream", elapsed)
	}
}

// The REAL album path, which the test above did not exercise: the PREFETCH goes
// through Local → Get (it wants the whole file), and the player then arrives
// through Stream. If Get's fetch cannot be tailed, the player waits it out and
// nothing above helps.
func TestTheStreamJoinsAPrefetchStartedByGet(t *testing.T) {
	c := cache(t)
	payload := make([]byte, 64<<10)

	// The prefetch: Get, in the background, exactly as remote.Prefetch does.
	go func() {
		_, _ = c.Get(context.Background(), "k", ".mp3", trickleFetch(payload, 4<<10, 25*time.Millisecond, nil))
	}()
	// Let it get going.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		_, running := c.inflight["k"]
		c.mu.Unlock()
		if running {
			break
		}
		time.Sleep(time.Millisecond)
	}

	start := time.Now()
	rc, err := c.Stream(context.Background(), "k", ".mp3", trickleFetch(payload, 4<<10, 25*time.Millisecond, nil))
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	if _, err := io.ReadFull(rc, make([]byte, 4<<10)); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Errorf("the player waited %v for its first bytes behind a prefetch — this is the album case", elapsed)
	}
}
