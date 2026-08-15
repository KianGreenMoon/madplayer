package backend

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Emptying the cache this device SEEDS from.
//
// The bug this closes: "Empty now" in Settings cleared the playback cache and
// left madshare's `cache/madnetwork/` untouched, so the half that grows with
// everything the mesh sends you was unreachable from the program that fetched
// it. Two caches, one button, one of them cleared.
//
// It works on the files, which docs/architecture/madnetwork-cache.md permits in
// as many words — the directory is authoritative and the index self-heals — so
// these tests are about the two rules that come with that permission: only
// finished blobs, and never a running transfer's partial.

func hashName(b byte) string { return strings.Repeat(string(b), 64) }

func seedingBackend(t *testing.T) (*Backend, string) {
	t.Helper()
	dir := t.TempDir()
	seed := filepath.Join(dir, "cache", "madnetwork")
	if err := os.MkdirAll(seed, 0o755); err != nil {
		t.Fatal(err)
	}
	return &Backend{dir: dir}, seed
}

func seedBlob(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSeedUsageCountsWhatIsOnDisk(t *testing.T) {
	be, seed := seedingBackend(t)

	if n, b := be.SeedUsage(); n != 0 || b != 0 {
		t.Fatalf("a fresh cache measured %d blob(s), %d byte(s)", n, b)
	}
	seedBlob(t, seed, hashName('a'), "12345")
	seedBlob(t, seed, hashName('b'), "678")
	n, b := be.SeedUsage()
	if n != 2 || b != 8 {
		t.Errorf("SeedUsage() = %d blob(s), %d byte(s); want 2 and 8", n, b)
	}
}

// The directory is derived the same way on both sides — config.MadnetworkCacheDir
// joins the data dir identically — so a wrong answer here would measure and
// clear a folder nobody writes to, and report zero forever.
func TestSeedDirIsWhereTheNodeWrites(t *testing.T) {
	be := &Backend{dir: "/data"}
	if got, want := be.SeedDir(), filepath.Join("/data", "cache", "madnetwork"); got != want {
		t.Errorf("SeedDir() = %q, want %q", got, want)
	}
}

func TestClearSeededEmptiesTheCacheAndReportsWhatItFreed(t *testing.T) {
	be, seed := seedingBackend(t)
	seedBlob(t, seed, hashName('a'), "12345")
	seedBlob(t, seed, hashName('b'), "678")

	freed, err := be.ClearSeeded()
	if err != nil {
		t.Fatalf("ClearSeeded: %v", err)
	}
	if freed != 8 {
		t.Errorf("freed = %d, want 8 — the number a person is watching go down", freed)
	}
	if n, _ := be.SeedUsage(); n != 0 {
		t.Errorf("%d blob(s) left", n)
	}
}

// The rule that keeps this safe: a `<hash>.part` belongs to a transfer that is
// RUNNING, and removing it breaks the download somebody is listening to. Only
// finished blobs — bare 64-hex names — are ours to remove.
func TestClearSeededLeavesARunningTransfersPartialAlone(t *testing.T) {
	be, seed := seedingBackend(t)
	done := hashName('a')
	partial := hashName('b') + ".part"
	seedBlob(t, seed, done, "finished")
	seedBlob(t, seed, partial, "still arriving")
	// Nothing else in that directory is ours either.
	seedBlob(t, seed, "notes.txt", "somebody else's file")

	// And none of it is attributed to the madnetwork: a partial weighs (real
	// bytes, ours) but is not a track yet, and a stray file is neither.
	if n, b := be.SeedUsage(); n != 1 || b != int64(len("finished")+len("still arriving")) {
		t.Errorf("SeedUsage() = %d track(s), %d byte(s); want 1 and only our two files", n, b)
	}

	if _, err := be.ClearSeeded(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(seed, done)); !os.IsNotExist(err) {
		t.Error("the finished blob is still there")
	}
	for _, name := range []string{partial, "notes.txt"} {
		if _, err := os.Stat(filepath.Join(seed, name)); err != nil {
			t.Errorf("%s was removed: %v", name, err)
		}
	}
}

// A clear that cannot remove says so, rather than reporting a freed disk that is
// still full. The loop keeps going past a failure — one stubborn file must not
// strand the rest — and the error survives to the end.
func TestClearSeededReportsWhatItCouldNotRemove(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root removes files out of a read-only directory")
	}
	be, seed := seedingBackend(t)
	seedBlob(t, seed, hashName('a'), "12345")
	seedBlob(t, seed, hashName('b'), "678")

	// Removal is governed by the DIRECTORY's write bit, which is the only way to
	// make one fail on a filesystem that does not care about the file's own.
	if err := os.Chmod(seed, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(seed, 0o755) })

	freed, err := be.ClearSeeded()
	if err == nil {
		t.Fatal("a clear that removed nothing reported success")
	}
	if freed != 0 {
		t.Errorf("freed = %d, want 0 — nothing actually went", freed)
	}
	if n, _ := be.SeedUsage(); n != 2 {
		t.Errorf("%d blob(s) left, want both still there", n)
	}

	// And when the directory allows it again, the same call empties it.
	if err := os.Chmod(seed, 0o755); err != nil {
		t.Fatal(err)
	}
	if freed, err := be.ClearSeeded(); err != nil || freed != 8 {
		t.Errorf("second ClearSeeded = %d, %v; want 8 and no error", freed, err)
	}
}

// An install whose node never fetched anything has no directory at all, and
// asking it to empty one is not an error — it is a page with a zero on it.
func TestClearSeededWithNothingFetchedIsHarmless(t *testing.T) {
	be := &Backend{dir: t.TempDir()}
	freed, err := be.ClearSeeded()
	if err != nil || freed != 0 {
		t.Errorf("ClearSeeded with nothing fetched = %d, %v", freed, err)
	}
	if n, b := be.SeedUsage(); n != 0 || b != 0 {
		t.Errorf("SeedUsage with no directory = %d, %d", n, b)
	}
}

// Turning the madnetwork off does not delete what it already fetched, so the
// clear must not need it: a cache you can only empty by switching a feature back
// on is a cache nobody empties.
func TestClearSeededWorksWithTheMeshOff(t *testing.T) {
	be, seed := seedingBackend(t)
	seedBlob(t, seed, hashName('a'), "left behind")
	if _, up := be.Mesh(); up {
		t.Fatal("this backend has a node — the test is not the mesh-off case")
	}

	freed, err := be.ClearSeeded()
	if err != nil {
		t.Fatal(err)
	}
	if freed != int64(len("left behind")) {
		t.Errorf("freed = %d, want the blob a previous session left", freed)
	}
}

// The boundary that matters most on that page: clearing caches must never reach
// music somebody kept ON PURPOSE. The two live in different places — the cache
// under the data dir, kept music in the folder the person chose — and this pins
// that ClearSeeded only ever touches the first.
func TestClearingTheCacheLeavesKeptMusicAlone(t *testing.T) {
	be, seed := seedingBackend(t)
	seedBlob(t, seed, hashName('a'), "cached")

	kept := filepath.Join(t.TempDir(), "Musik", "madplayer", "Artist", "Album")
	if err := os.MkdirAll(kept, 0o755); err != nil {
		t.Fatal(err)
	}
	track := filepath.Join(kept, "01 - Song.flac")
	if err := os.WriteFile(track, []byte("kept on purpose"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := be.ClearSeeded(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(track); err != nil {
		t.Fatalf("kept music was removed by a cache clear: %v", err)
	}
}
