package blobcache

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The claim this package exists to make good on: a reader can start on a blob
// that is still arriving, and it blocks at the tail rather than reporting an end
// that has not happened.

func cache(t *testing.T) *Cache {
	t.Helper()
	c, err := Open(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// trickleFetch writes the payload in pieces, pausing between them, and reports
// how far it has got.
func trickleFetch(payload []byte, piece int, gap time.Duration, wrote chan<- int) Fetch {
	return func(ctx context.Context, w io.Writer) error {
		for i := 0; i < len(payload); i += piece {
			end := i + piece
			if end > len(payload) {
				end = len(payload)
			}
			if _, err := w.Write(payload[i:end]); err != nil {
				return err
			}
			if wrote != nil {
				wrote <- end
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(gap):
			}
		}
		return nil
	}
}

// The whole point: bytes are readable before the fetch has finished writing
// them. Without this, a remote track is silent until every byte has landed.
func TestAStreamIsReadableBeforeTheFetchFinishes(t *testing.T) {
	c := cache(t)
	payload := make([]byte, 64<<10)
	for i := range payload {
		payload[i] = byte(i)
	}

	start := time.Now()
	rc, err := c.Stream(context.Background(), "k", ".mp3", trickleFetch(payload, 4<<10, 20*time.Millisecond, nil))
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()

	first := make([]byte, 4<<10)
	n, err := io.ReadFull(rc, first)
	if err != nil {
		t.Fatalf("read %d bytes: %v", n, err)
	}
	elapsed := time.Since(start)

	// Sixteen pieces, 20ms apart: the whole fetch takes ~320ms. Getting the
	// first piece in a small fraction of that is the claim.
	if elapsed > 150*time.Millisecond {
		t.Errorf("first bytes took %v — that is the whole download, not a stream", elapsed)
	}
	for i := range first {
		if first[i] != byte(i) {
			t.Fatalf("byte %d is %d, want %d", i, first[i], byte(i))
		}
	}
}

// The reader must WAIT at the tail. Returning io.EOF there would tell a decoder
// the track had ended, and it would stop a few hundred milliseconds in.
func TestTheTailWaitsInsteadOfReportingTheEnd(t *testing.T) {
	c := cache(t)
	payload := make([]byte, 32<<10)

	rc, err := c.Stream(context.Background(), "k", ".mp3", trickleFetch(payload, 8<<10, 30*time.Millisecond, nil))
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()

	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(got) != len(payload) {
		t.Errorf("read %d bytes of %d — the reader stopped at a tail that was not the end", len(got), len(payload))
	}
}

// A growing file must NOT satisfy io.Seeker: that is the flag the decoders read
// to choose their streaming path, and it is what stops beep's mp3 decoder from
// panicking on a Seek it cannot serve.
func TestAStreamIsDeliberatelyNotSeekable(t *testing.T) {
	c := cache(t)
	rc, err := c.Stream(context.Background(), "k", ".mp3", trickleFetch(make([]byte, 1<<10), 1<<10, 0, nil))
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()

	if _, ok := rc.(io.Seeker); ok {
		t.Fatal("a growing file reported itself as seekable — decoders would take their whole-file path")
	}
}

// A blob already on disk is handed over as an ordinary file, so replaying a
// track keeps every bit of its seeking.
func TestACachedBlobComesBackSeekable(t *testing.T) {
	c := cache(t)
	ctx := context.Background()
	payload := []byte("already here")

	if _, err := c.Get(ctx, "k", ".mp3", trickleFetch(payload, len(payload), 0, nil)); err != nil {
		t.Fatal(err)
	}
	rc, err := c.Stream(ctx, "k", ".mp3", func(context.Context, io.Writer) error {
		t.Error("a cached blob was fetched again")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()

	if _, ok := rc.(io.Seeker); !ok {
		t.Error("a cached blob came back unseekable — replaying it would lose scrubbing")
	}
	got, _ := io.ReadAll(rc)
	if string(got) != string(payload) {
		t.Errorf("read %q, want %q", got, payload)
	}
}

// A fetch that dies mid-stream must surface as an ERROR, not as the end of the
// track: the difference is whether the player reports a broken track or quietly
// moves on as though it had finished.
func TestAFailedFetchIsAnErrorAndNotAnEnding(t *testing.T) {
	c := cache(t)
	boom := errors.New("the server went away")

	rc, err := c.Stream(context.Background(), "k", ".mp3", func(ctx context.Context, w io.Writer) error {
		w.Write(make([]byte, 8<<10))
		time.Sleep(20 * time.Millisecond)
		return boom
	})
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()

	_, err = io.ReadAll(rc)
	if !errors.Is(err, boom) {
		t.Errorf("err = %v, want the fetch's own error", err)
	}
}

// A half-written file must never survive as a playable track — it would decode
// into noise and look like a corrupt original. The streaming path has its own
// version of this because it removes the part file while a reader still holds
// it open, which is a different sequence from the waiting path's.
func TestAFailedStreamLeavesNothingBehind(t *testing.T) {
	dir := t.TempDir()
	c, err := Open(dir, 0)
	if err != nil {
		t.Fatal(err)
	}

	rc, err := c.Stream(context.Background(), "k", ".mp3", func(ctx context.Context, w io.Writer) error {
		w.Write(make([]byte, 1<<10))
		return errors.New("nope")
	})
	if err != nil {
		t.Fatal(err)
	}
	io.ReadAll(rc)
	rc.Close()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		t.Errorf("%s survived a failed fetch", filepath.Base(e.Name()))
	}
}

// Closing the reader is what says nobody is listening. A person skipping through
// ten tracks should not be downloading ten.
func TestClosingTheReaderStopsTheFetch(t *testing.T) {
	c := cache(t)
	stopped := make(chan struct{})

	rc, err := c.Stream(context.Background(), "k", ".mp3", func(ctx context.Context, w io.Writer) error {
		w.Write(make([]byte, 1<<10))
		<-ctx.Done()
		close(stopped)
		return ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(rc, make([]byte, 1<<10)); err != nil {
		t.Fatal(err)
	}
	rc.Close()

	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("the fetch kept running after the last reader went away")
	}
}

// A reader parked at the tail of a cancelled fetch would otherwise wait forever
// for bytes nobody is going to write.
func TestACancelledFetchWakesTheReader(t *testing.T) {
	c := cache(t)
	ctx, cancel := context.WithCancel(context.Background())

	rc, err := c.Stream(ctx, "k", ".mp3", func(ctx context.Context, w io.Writer) error {
		w.Write(make([]byte, 512))
		<-ctx.Done()
		return ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()

	if _, err := io.ReadFull(rc, make([]byte, 512)); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := io.ReadAll(rc)
		done <- err
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a reader was left parked on a cancelled fetch")
	}
}
