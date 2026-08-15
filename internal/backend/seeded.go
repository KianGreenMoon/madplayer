package backend

import (
	"os"
	"path/filepath"
)

// The cache this device SEEDS from, as opposed to the one it plays from.
//
// A swarm fetch stores a blob twice, and the two copies are not redundant —
// they are the two caches docs/ui/madplayer.md describes, each swept by its own
// enforcer under the same ceiling:
//
//   - madshare's `cache/madnetwork/`, hash-named and extensionless. It is the
//     only directory federation seeds from and the only one Holdings advertises,
//     so it is what makes this device useful to the household.
//   - this client's `remote/`, with a file extension on it, because the decoders
//     pick by one (internal/blobcache).
//
// A person emptying "the cache" means both, and until 2026-08-15 the button
// reached only the second — which is the half that stops growing when you stop
// playing, while the seeded half grows with everything the mesh sends you.

// SeedDir is where the blobs this device seeds live.
//
// Derived rather than asked for, because it is derived on the other side too
// (config.MadnetworkCacheDir joins the data dir the same way), and this package
// is the one that built that config.
func (b *Backend) SeedDir() string { return filepath.Join(b.dir, "cache", "madnetwork") }

// SeedUsage is how many blobs this device is seeding and what they weigh.
//
// It walks the directory, which is the same thing federation does to decide what
// to advertise — the directory is the truth and the index describes it
// (docs/architecture/madnetwork-cache.md §"The model"). So this cannot report a
// blob that is not there, and it costs one readdir.
//
// Partial files are counted with the rest. They are real bytes on a real disk,
// and a size that omitted them would under-report exactly when a person is
// asking because the disk is full.
func (b *Backend) SeedUsage() (count int, bytes int64) {
	entries, err := os.ReadDir(b.SeedDir())
	if err != nil {
		return 0, 0
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		bytes += info.Size()
		count++
	}
	return count, bytes
}

// ClearSeeded stops this device seeding what it has fetched, and gives the disk
// back.
//
// The blobs are not lost to anybody: a cache is by definition re-fetchable, and
// everything here came from somebody else who still has it. What it does mean is
// that this device stops being one of the holders for a while, which is the
// honest cost of the button and belongs next to it on screen.
//
// It goes one blob at a time through the facade rather than removing the
// directory, and the difference is not fussiness. EvictCached leaves the `.part`
// of an in-flight transfer alone — so clearing the cache cannot break the track
// that is playing — and it drops the node's memoized manifest with the bytes,
// which a directory removal would leave behind.
//
// A blob that will not go is reported and the rest still go: one stubborn file
// must not leave the disk full.
func (b *Backend) ClearSeeded() (freed int64, err error) {
	if b.net == nil {
		return 0, nil
	}
	dir := b.SeedDir()
	for _, hash := range b.net.Holdings() {
		size := int64(0)
		if info, statErr := os.Stat(filepath.Join(dir, hash)); statErr == nil {
			size = info.Size()
		}
		if evErr := b.net.EvictCached(hash); evErr != nil {
			err = evErr
			continue
		}
		freed += size
	}
	return freed, err
}
