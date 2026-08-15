package blobcache

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A fetch that blocks until cancelled, then takes a moment to actually return —
// which is what a real fetch does: cancellation propagates through an HTTP body
// read or a mesh chunk request, not instantaneously. The delay is the window
// every test in this file is about.
func slowDying(started chan<- struct{}) Fetch {
	return func(ctx context.Context, w io.Writer) error {
		close(started)
		<-ctx.Done()
		time.Sleep(150 * time.Millisecond)
		return ctx.Err()
	}
}

// The last reader closes; a new caller for the same track arrives while the
// old fetch is still tearing down (click away, click back). It must get a
// fresh fetch — before the fix it joined the dying call and inherited its
// context.Canceled, which the player reads as "the user skipped", so the
// click silently did nothing (found + reproduced 2026-08-15).
func TestAJoinerAfterTheLastDropGetsAFreshFetch(t *testing.T) {
	c, err := Open(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}

	started := make(chan struct{})
	r1, err := c.Stream(context.Background(), "k1", ".mp3", slowDying(started))
	if err != nil {
		t.Fatal(err)
	}
	<-started
	r1.Close() // last waiter leaves; the fetch is being cancelled

	r2, err := c.Stream(context.Background(), "k1", ".mp3", func(ctx context.Context, w io.Writer) error {
		_, err := w.Write([]byte("hello"))
		return err
	})
	if err != nil {
		t.Fatalf("second Stream: %v", err)
	}
	defer r2.Close()

	buf := make([]byte, 5)
	if _, err := io.ReadFull(r2, buf); err != nil {
		if errors.Is(err, context.Canceled) {
			t.Fatalf("the second caller was attached to the dying fetch: %v", err)
		}
		t.Fatalf("read: %v", err)
	}
	if string(buf) != "hello" {
		t.Fatalf("read %q, want the fresh fetch's bytes", buf)
	}
}

// The last reader closes while the fetch is still dying, so drop cannot see the
// error yet. The fetch itself must then remove its part file — before the fix
// nobody did, and the orphan sat on disk until the next launch's reaper.
func TestAPartFileIsRemovedWhenTheFetchDiesAfterTheLastDrop(t *testing.T) {
	dir := t.TempDir()
	c, err := Open(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	r1, err := c.Stream(context.Background(), "k2", ".mp3", slowDying(started))
	if err != nil {
		t.Fatal(err)
	}
	<-started
	r1.Close()

	deadline := time.Now().Add(5 * time.Second)
	for {
		if !anyPartFile(t, dir) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the failed fetch's .part file is still on disk")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func anyPartFile(t *testing.T, dir string) bool {
	t.Helper()
	des, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, de := range des {
		if strings.HasSuffix(de.Name(), ".part") {
			return true
		}
	}
	return false
}

// The startup reaper still catches the part names, now that they carry a
// sequence number: the suffix is what it matches on, and a rename to a
// suffixless name is what completion means.
func TestSequencedPartFilesAreReapedAtOpen(t *testing.T) {
	dir := t.TempDir()
	stale := filepath.Join(dir, "k3.mp3.7.part")
	if err := os.WriteFile(stale, []byte("half"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale sequenced part survived Open: %v", err)
	}
}
