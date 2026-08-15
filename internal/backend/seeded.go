package backend

import (
	"os"
	"path/filepath"
	"strings"
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
//
// **Both are this device's own directories**, under the data dir this package
// created, which is why a player may empty them without asking anybody. A
// SERVER's cache is a different thing with a different answer: it belongs to
// whoever administers that server, and controlling it from here would be a
// remote-administration feature rather than a settings page.
//
// This works on the FILES, deliberately, and that is sound rather than a
// shortcut. docs/architecture/madnetwork-cache.md §"The model: the directory is
// the truth, the index is derived" makes the directory authoritative and the
// index descriptive, and §"Files deleted behind the server's back" says what
// happens when something removes a file underneath the node: nothing dangerous,
// ever — seeding re-lists the directory on every request, so a blob that is gone
// is never advertised, and the index self-heals as it is read.
//
// Two rules come from that section and are load-bearing here:
//
//   - **Only hash-named files.** A `<hash>.part` belongs to a transfer that is
//     RUNNING, and removing it breaks the download somebody is listening to.
//     Nothing else in this directory is ours to touch.
//   - **A file being read is safe to unlink.** POSIX keeps the open descriptor
//     alive, so clearing while a track plays cannot stop it mid-song.

// SeedDir is where the blobs this device seeds live.
//
// Derived rather than asked for, because it is derived on the other side too
// (config.MadnetworkCacheDir joins the data dir the same way), and this package
// is the one that built that config.
func (b *Backend) SeedDir() string { return filepath.Join(b.dir, "cache", "madnetwork") }

// SeedUsage is how many tracks this device is seeding and what the cache weighs.
//
// It walks the directory, which is the same thing federation does to decide what
// to advertise — the directory is the truth and the index describes it
// (docs/architecture/madnetwork-cache.md §"The model"). So this cannot report a
// blob that is not there, and it costs one readdir.
//
// The two numbers deliberately count different things. COUNT is finished blobs,
// because that is what "shared with the madnetwork" means — a half-arrived track
// is not being served to anybody. BYTES includes the partials, because they are
// real bytes on a real disk and a size that omitted them would under-report
// exactly when somebody is asking because the disk is full.
//
// Anything else in that directory is nobody's business here: it is not counted,
// not weighed and not removed. Attributing a stray file to the madnetwork would
// be a number the program cannot act on.
func (b *Backend) SeedUsage() (count int, bytes int64) {
	entries, err := os.ReadDir(b.SeedDir())
	if err != nil {
		return 0, 0
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		finished := isBlobName(name)
		if !finished && !isBlobName(strings.TrimSuffix(name, ".part")) {
			continue
		}
		if info, err := e.Info(); err == nil {
			bytes += info.Size()
		}
		if finished {
			count++
		}
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
// It runs whether or not the mesh is up, on purpose. Turning the madnetwork off
// does not delete what it already fetched, and a cache you can only empty by
// first switching a feature back on is a cache nobody empties.
//
// A blob that will not go is reported and the rest still go: one stubborn file
// must not leave the disk full.
func (b *Backend) ClearSeeded() (freed int64, err error) {
	dir := b.SeedDir()
	entries, readErr := os.ReadDir(dir)
	if readErr != nil {
		if os.IsNotExist(readErr) {
			return 0, nil // nothing was ever fetched here
		}
		return 0, readErr
	}
	for _, e := range entries {
		if e.IsDir() || !isBlobName(e.Name()) {
			continue
		}
		size := int64(0)
		if info, statErr := e.Info(); statErr == nil {
			size = info.Size()
		}
		if rmErr := os.Remove(filepath.Join(dir, e.Name())); rmErr != nil && !os.IsNotExist(rmErr) {
			err = rmErr
			continue
		}
		freed += size
	}
	return freed, err
}

// isBlobName reports whether a file in the cache directory is a finished blob:
// a bare content hash, 64 lowercase hex characters.
//
// It is the same test federation applies before touching that directory
// (isBlobHash), and it is what keeps a `<hash>.part` — the partial of a transfer
// that is running — out of everything above.
func isBlobName(name string) bool {
	if len(name) != 64 {
		return false
	}
	for _, c := range name {
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f':
		default:
			return false
		}
	}
	return true
}
