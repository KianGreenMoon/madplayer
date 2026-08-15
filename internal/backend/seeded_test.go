package backend

import (
	"os"
	"path/filepath"
	"testing"
)

// Emptying the cache this device SEEDS from.
//
// The bug this closes: "Empty now" in Settings cleared the playback cache and
// left madshare's `cache/madnetwork/` untouched, so the half that grows with
// everything the mesh sends you was unreachable from the program that fetched
// it. Two caches, one button, one of them cleared.

// evictingNet counts what it was asked to drop and does it, the way the real
// node does — the directory is the truth on both sides.
type evictingNet struct {
	stubNet
	dir  string
	fail map[string]error
	// asked records the order, because "empty the cache" is a loop over
	// Holdings and a caller that asked for something not in it is confused.
	asked []string
}

func (n *evictingNet) Holdings() []string {
	entries, err := os.ReadDir(n.dir)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

func (n *evictingNet) EvictCached(hash string) error {
	n.asked = append(n.asked, hash)
	if err := n.fail[hash]; err != nil {
		return err
	}
	return os.Remove(filepath.Join(n.dir, hash))
}

func seedingBackend(t *testing.T) (*Backend, *evictingNet) {
	t.Helper()
	dir := t.TempDir()
	net := &evictingNet{dir: filepath.Join(dir, "cache", "madnetwork")}
	if err := os.MkdirAll(net.dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return &Backend{dir: dir, net: net}, net
}

func seedBlob(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSeedUsageCountsWhatIsOnDisk(t *testing.T) {
	be, net := seedingBackend(t)

	if n, b := be.SeedUsage(); n != 0 || b != 0 {
		t.Fatalf("a fresh cache measured %d blob(s), %d byte(s)", n, b)
	}
	seedBlob(t, net.dir, "aa", "12345")
	seedBlob(t, net.dir, "bb", "678")
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
	be, net := seedingBackend(t)
	seedBlob(t, net.dir, "aa", "12345")
	seedBlob(t, net.dir, "bb", "678")

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
	if len(net.asked) != 2 {
		t.Errorf("asked to evict %v, want both blobs", net.asked)
	}
}

// One stubborn file must not leave the disk full: the rest still go, and the
// failure is still reported.
func TestClearSeededKeepsGoingPastOneThatWillNotGo(t *testing.T) {
	be, net := seedingBackend(t)
	seedBlob(t, net.dir, "aa", "12345")
	seedBlob(t, net.dir, "bb", "678")
	net.fail = map[string]error{"aa": os.ErrPermission}

	freed, err := be.ClearSeeded()
	if err == nil {
		t.Fatal("a blob that would not go was reported as success")
	}
	if freed != 3 {
		t.Errorf("freed = %d, want the 3 bytes that did go", freed)
	}
	if n, _ := be.SeedUsage(); n != 1 {
		t.Errorf("%d blob(s) left, want only the stubborn one", n)
	}
}

// An install with no mesh has no second cache, and asking it to empty one is
// not an error — it is a page with one section instead of two.
func TestClearSeededWithNoMeshIsHarmless(t *testing.T) {
	be := &Backend{dir: t.TempDir()}
	freed, err := be.ClearSeeded()
	if err != nil || freed != 0 {
		t.Errorf("ClearSeeded with no node = %d, %v", freed, err)
	}
}

// The boundary that matters most on that page: clearing caches must never reach
// music somebody kept ON PURPOSE. The two live in different places — the cache
// under the data dir, kept music in the folder the person chose — and this pins
// that ClearSeeded only ever touches the first.
func TestClearingTheCacheLeavesKeptMusicAlone(t *testing.T) {
	be, net := seedingBackend(t)
	seedBlob(t, net.dir, "aa", "cached")

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
