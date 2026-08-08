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
	be, err := Open(context.Background(), t.TempDir(), log.New(io.Discard, "", 0), Options{})
	if err != nil {
		t.Fatalf("open backend: %v", err)
	}
	t.Cleanup(be.Close)
	return be
}

func TestAFreshInstallGetsThisClientsDefault(t *testing.T) {
	be := openBackend(t)

	got, err := be.CacheCeiling(context.Background())
	if err != nil {
		t.Fatalf("CacheCeiling: %v", err)
	}
	want := int64(DefaultCacheMB) << 20
	if got.Default != want || got.Effective != want {
		t.Errorf("fresh ceiling = %+v, want default and effective %d", got, want)
	}
	// Supplied as CONFIG, not written into the settings: nothing is overridden
	// yet, which is what makes clearing an override later land back here.
	if got.Override != nil {
		t.Errorf("a fresh install has an override of %d; the default must be config, not a stored value", *got.Override)
	}
}

func TestTheChosenCeilingSurvivesAndClearingRestoresTheDefault(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	pin := func(n int64) *int64 { return &n }

	be, err := Open(ctx, dir, log.New(io.Discard, "", 0), Options{})
	if err != nil {
		t.Fatal(err)
	}
	// 0 means no limit, and is the sharpest case: a restart must not read it as
	// "unset" and helpfully put the default back.
	if err := be.SetCacheCeiling(ctx, pin(0)); err != nil {
		t.Fatalf("SetCacheCeiling: %v", err)
	}
	be.Close()

	again, err := Open(ctx, dir, log.New(io.Discard, "", 0), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()

	got, err := again.CacheCeiling(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.Override == nil || *got.Override != 0 || got.Effective != 0 {
		t.Errorf("after restart: %+v, want the chosen 0 (no limit)", got)
	}

	if err := again.SetCacheCeiling(ctx, pin(512<<20)); err != nil {
		t.Fatal(err)
	}
	if got, err = again.CacheCeiling(ctx); err != nil || got.Effective != 512<<20 {
		t.Errorf("ceiling = %+v (%v), want 512 MiB", got, err)
	}

	// Clearing lands on the client's default, not on "no limit" — which is only
	// true because that default is config rather than a value written once.
	if err := again.SetCacheCeiling(ctx, nil); err != nil {
		t.Fatal(err)
	}
	got, err = again.CacheCeiling(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.Override != nil || got.Effective != int64(DefaultCacheMB)<<20 {
		t.Errorf("after clearing: %+v, want the default back", got)
	}
}
