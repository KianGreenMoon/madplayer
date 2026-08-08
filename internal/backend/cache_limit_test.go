package backend

import (
	"context"
	"io"
	"log"
	"testing"
)

// The download ceiling is madshare's runtime setting, reached through the
// embedded backend — not a key in this client's config file. These pin the two
// properties that makes worth having: a fresh install gets a stated number, and
// the number the person then chooses is the one that survives.

func openBackend(t *testing.T) *Backend {
	t.Helper()
	be, err := Open(context.Background(), t.TempDir(), log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatalf("open backend: %v", err)
	}
	t.Cleanup(be.Close)
	return be
}

func TestFirstRunWritesAStatedCacheLimit(t *testing.T) {
	be := openBackend(t)

	got, err := be.CacheLimit(context.Background())
	if err != nil {
		t.Fatalf("CacheLimit: %v", err)
	}
	// Written, not assumed: the settings field shows a real value the person can
	// see and change, rather than a hidden fallback contradicting what it says.
	if got != DefaultCacheLimit {
		t.Errorf("first run limit = %d, want the stated default %d", got, DefaultCacheLimit)
	}
}

func TestTheChosenCacheLimitSurvives(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	be, err := Open(ctx, dir, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	// 0 means no limit, and is the sharpest case: a second run must not read it
	// as "unset" and helpfully put the default back.
	if err := be.SetCacheLimit(ctx, 0); err != nil {
		t.Fatalf("SetCacheLimit: %v", err)
	}
	be.Close()

	again, err := Open(ctx, dir, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()

	got, err := again.CacheLimit(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got != 0 {
		t.Errorf("limit after restart = %d, want the chosen 0 (no limit)", got)
	}

	if err := again.SetCacheLimit(ctx, 512<<20); err != nil {
		t.Fatal(err)
	}
	if got, err = again.CacheLimit(ctx); err != nil || got != 512<<20 {
		t.Errorf("limit = %d (%v), want 512 MiB", got, err)
	}
}
