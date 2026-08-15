// Package blobcache holds audio fetched from a remote server on local disk.
//
// It keeps a copy so that playing a track twice costs one download, and so that
// a track survives the network going away. It does NOT exist because the
// decoders need a whole file — that was believed for a long time and it is
// wrong. go-mp3 walks every frame header only when its source is an io.Seeker,
// and beep's flac picks its seeking parser on the same test; hand either a
// reader that does not seek and it starts on the first fraction of a percent.
// See stream.go, which is what a remote track is played through now.
//
// The DIRECTORY IS AUTHORITATIVE and there is no index — the same rule the
// server's madnetwork cache settled on (docs/architecture/madnetwork-cache.md).
// Last use is the file's mtime, touched on every hit: an index would be a second
// thing to keep in agreement with the disk, and disagreeing with the disk is the
// only way a cache can lie.
package blobcache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Fetch writes one blob's bytes. It is given a context that is cancelled when
// every caller waiting on the fetch has gone away.
type Fetch func(ctx context.Context, w io.Writer) error

// Cache is a size-capped directory of fetched audio.
type Cache struct {
	dir string

	mu       sync.Mutex
	limit    int64 // bytes; 0 = no ceiling
	inflight map[string]*call
}

// call is one fetch several callers may be waiting on.
type call struct {
	done    chan struct{}
	cancel  context.CancelFunc
	waiters int
	path    string
	err     error

	// prog and part let a LATER caller join a fetch that is already running and
	// read it as it lands, rather than waiting for it to finish. That is the
	// ordinary case on an album: track 1 is playing, track 2 is being prefetched,
	// track 1 ends — and without this the player waits out track 2's whole
	// download, which is streaming buying nothing for every track after the
	// first. See stream.go.
	prog *progress
	part string
}

// Open prepares dir as a cache. limit is a ceiling in bytes; 0 means none.
//
// Abandoned .part files are reaped here, unconditionally: a just-started process
// is writing nothing, so every partial in the directory is by definition left
// over from a previous run. That is the same reasoning the server's cache uses,
// and it is only sound at startup.
func Open(dir string, limit int64) (*Cache, error) {
	if dir == "" {
		return nil, errors.New("blobcache: no directory")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	c := &Cache{dir: dir, limit: limit, inflight: map[string]*call{}}
	c.reapPartials()
	return c, nil
}

// Dir is where the cache lives.
func (c *Cache) Dir() string { return c.dir }

// SetLimit changes the ceiling and enforces it immediately — a person who has
// just lowered it expects the disk to give the space back now, not at the next
// download.
func (c *Cache) SetLimit(limit int64) {
	c.mu.Lock()
	c.limit = limit
	c.mu.Unlock()
	c.evict("")
}

// Limit is the current ceiling in bytes; 0 means none.
func (c *Cache) Limit() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.limit
}

// Key turns anything addressing a blob into a file name. A content hash — which
// is what a madshare play URL carries — is used as it is, so the same audio
// fetched from two servers is stored once.
func Key(s string) string {
	if isHash(s) {
		return strings.ToLower(s)
	}
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func isHash(s string) bool {
	if len(s) < 32 || len(s) > 128 {
		return false
	}
	for _, r := range s {
		if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
			return false
		}
	}
	return true
}

// Lookup reports a cached blob's path without fetching anything, and counts the
// hit so eviction sees it. It is what an "is this already here?" check uses —
// deciding whether a track can be played offline, for one.
func (c *Cache) Lookup(key, ext string) (string, bool) {
	path := c.path(key, ext)
	if _, err := os.Stat(path); err != nil {
		return "", false
	}
	c.touch(path)
	return path, true
}

// Get returns the local path of a blob, fetching it if it is not here yet.
//
// Concurrent callers for the same key share one fetch: the queue prefetching the
// next track while the user clicks that very track must not download it twice.
// A caller whose context is cancelled stops waiting; the fetch itself continues
// only while somebody is still waiting on it, so skipping past a track abandons
// its download rather than paying for it in the background.
func (c *Cache) Get(ctx context.Context, key, ext string, fetch Fetch) (string, error) {
	if path, ok := c.Lookup(key, ext); ok {
		return path, nil
	}

	cl, err := c.begin(ctx, key, ext, fetch)
	if err != nil {
		return "", err
	}
	// Leaving is what says nobody is listening any more. Stopping then is the
	// point: a person skipping through ten tracks should not be downloading ten.
	defer c.drop(cl)

	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-cl.done:
		return cl.path, cl.err
	}
}

// Size is what the cache currently occupies, in bytes.
func (c *Cache) Size() (int64, error) {
	entries, err := c.entries()
	if err != nil {
		return 0, err
	}
	var total int64
	for _, e := range entries {
		total += e.size
	}
	return total, nil
}

// Clear empties the cache. Partial downloads go too; a live fetch will fail its
// rename and report it, which is honest — the person asked for the directory to
// be emptied while something was writing to it.
func (c *Cache) Clear() error {
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		return err
	}
	var firstErr error
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if err := os.Remove(filepath.Join(c.dir, e.Name())); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

type entry struct {
	name string
	size int64
	used time.Time
}

func (c *Cache) entries() ([]entry, error) {
	des, err := os.ReadDir(c.dir)
	if err != nil {
		return nil, err
	}
	out := make([]entry, 0, len(des))
	for _, de := range des {
		if de.IsDir() || strings.HasSuffix(de.Name(), ".part") {
			continue
		}
		info, err := de.Info()
		if err != nil {
			continue // vanished under us; not this walk's problem
		}
		out = append(out, entry{name: de.Name(), size: info.Size(), used: info.ModTime()})
	}
	return out, nil
}

// evict deletes least-recently-used blobs until the cache is under its ceiling.
// keep names a file that must survive whatever happens.
func (c *Cache) evict(keep string) {
	limit := c.Limit()
	if limit <= 0 {
		return
	}
	entries, err := c.entries()
	if err != nil {
		return
	}
	var total int64
	for _, e := range entries {
		total += e.size
	}
	if total <= limit {
		return
	}
	// Oldest use first. Ties break by name so the order is deterministic — two
	// files written in the same filesystem timestamp tick are otherwise removed
	// in whatever order the directory happened to list them.
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].used.Equal(entries[j].used) {
			return entries[i].name < entries[j].name
		}
		return entries[i].used.Before(entries[j].used)
	})
	for _, e := range entries {
		if total <= limit {
			return
		}
		if e.name == keep {
			continue
		}
		// A removal failure is not fatal: on Windows the file may be open in the
		// decoder right now. Skipping it leaves the cache briefly over its
		// ceiling, which is better than refusing to play.
		if err := os.Remove(filepath.Join(c.dir, e.name)); err == nil {
			total -= e.size
		}
	}
}

// reapPartials removes leftovers from a previous run.
func (c *Cache) reapPartials() {
	des, err := os.ReadDir(c.dir)
	if err != nil {
		return
	}
	for _, de := range des {
		if !de.IsDir() && strings.HasSuffix(de.Name(), ".part") {
			_ = os.Remove(filepath.Join(c.dir, de.Name()))
		}
	}
}

// touch records a hit. mtime is the clock, not atime: atime is unreliable on a
// relatime mount, which is nearly all of them.
func (c *Cache) touch(path string) {
	now := time.Now()
	_ = os.Chtimes(path, now, now)
}

func (c *Cache) path(key, ext string) string {
	if ext != "" && !strings.HasPrefix(ext, ".") {
		ext = "." + ext
	}
	return filepath.Join(c.dir, fmt.Sprintf("%s%s", key, ext))
}
