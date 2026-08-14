// Package artwork finds the cover for a piece of music and hands back a small
// decoded image.
//
// It reads the FILE, not the library index. That is a deliberate divergence from
// the rule that the backend decides everything: madshare's ingest does extract
// cover images, but its embedder facade reports only whether an album HAS one
// (`database.AlbumEntry.HasImage`) and offers no way to reach the bytes — there
// is no image twin of `Library.BlobPath`. Reading the audio file's own tags is
// not a re-derivation of a server rule, it is the only route this client has,
// and it has two properties worth keeping even after a facade call exists: it
// works with the network unplugged, and it cannot disagree with the file.
//
// Nothing here blocks. A layout function runs sixty times a second, so [Cache.Get]
// answers from memory or answers "not yet" and reads the file on a background
// goroutine, calling OnLoad when there is something new to paint.
package artwork

import (
	"bytes"
	"image"
	"os"
	"path/filepath"
	"strings"
	"sync"

	// The decoders register themselves. GIF is in for the same reason the others
	// are: somebody's collection has one, and refusing it buys nothing.
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	"github.com/dhowden/tag"
	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

// MaxDimension is what a cover is scaled down to before it is kept.
//
// The largest place one is painted is an album header at ~120dp, so 320px covers
// a 2× display with room to spare — while an untouched 3000×3000 booklet scan is
// 36 MB of pixels for a thumbnail, and a folder full of them is how a music
// player comes to use a gigabyte of RAM.
const MaxDimension = 320

// maxEntries bounds the cache. At MaxDimension that is ~13 MB of pixels, which
// is the right order for a program whose job is audio.
const maxEntries = 64

// coverNames are the files a cover is looked for under, beside the music. The
// list is the one every other player uses, matched case-insensitively because
// "Cover.jpg" and "cover.jpg" are the same intention.
var coverNames = []string{"cover", "folder", "front", "album", "albumart", "artwork", "thumb"}

// coverExts are the containers those files may be in — the same set the decoders
// above were imported for.
var coverExts = map[string]bool{".jpg": true, ".jpeg": true, ".png": true, ".webp": true, ".gif": true}

// Cache resolves and holds covers, keyed by the audio file they belong to.
//
// The zero value is not usable; call New.
type Cache struct {
	// OnLoad is called from a background goroutine when a cover has finished
	// loading and the screen should be repainted. For Gio that means
	// Window.Invalidate and nothing else.
	OnLoad func()

	mu      sync.Mutex
	entries map[string]*entry
	// order is insertion order, for the eviction that keeps the cache bounded.
	// A strict LRU would need a touch on every frame; a music player looks at a
	// handful of covers at a time, so oldest-first is close enough and costs
	// nothing to maintain.
	order []string
	// dirs memoizes the cover file found beside a directory — an album's twelve
	// tracks must not each list the same folder. An empty string means "looked,
	// found nothing", which is why presence in the map is the answer and not the
	// value.
	dirs map[string]string
}

// entry is one cover, in one of three states: loading (done false), found
// (img non-nil) or absent (done, img nil). The third is a real answer and is
// cached like the others — a folder of untagged files must not be re-read on
// every frame just because it has no art.
type entry struct {
	img  image.Image
	done bool
}

// New returns an empty cache.
func New() *Cache {
	return &Cache{entries: map[string]*entry{}, dirs: map[string]string{}}
}

// Get returns the cover for an audio file.
//
// The second result distinguishes "still reading" from "there is none": both
// paint nothing, but only one of them is worth waiting for, and a caller that
// conflated them would flash a placeholder in and out on every repaint.
func (c *Cache) Get(path string) (img image.Image, settled bool) {
	if path == "" {
		return nil, true
	}
	c.mu.Lock()
	if e, ok := c.entries[path]; ok {
		c.mu.Unlock()
		return e.img, e.done
	}
	c.entries[path] = &entry{}
	c.order = append(c.order, path)
	c.evictLocked()
	c.mu.Unlock()

	go c.load(path)
	return nil, false
}

// evictLocked drops the oldest entries once the cache is over its bound.
func (c *Cache) evictLocked() {
	for len(c.order) > maxEntries {
		delete(c.entries, c.order[0])
		c.order = c.order[1:]
	}
}

// load reads a cover and stores the answer, including the answer "none".
func (c *Cache) load(path string) {
	img := c.find(path)
	if img != nil {
		img = shrink(img)
	}

	c.mu.Lock()
	// The entry can have been evicted while this ran, in which case storing it
	// would resurrect a key nothing is waiting for.
	if e, ok := c.entries[path]; ok {
		e.img, e.done = img, true
	}
	c.mu.Unlock()

	if c.OnLoad != nil {
		c.OnLoad()
	}
}

// find is the resolution order: the file's own tags first, then the folder.
//
// Embedded art wins because it is the artist's answer for THIS track — a
// compilation in one folder has twelve different covers and one folder.jpg, and
// the folder image is the fallback for exactly the untagged case where nothing
// better exists.
func (c *Cache) find(path string) image.Image {
	if img := embedded(path); img != nil {
		return img
	}
	if cover := c.folderCover(filepath.Dir(resolve(path))); cover != "" {
		if img := decodeFile(cover); img != nil {
			return img
		}
	}
	return nil
}

// resolve follows the link a scanned track is reached through.
//
// This is load-bearing rather than tidy. madshare imports a folder IN PLACE by
// writing one symlink per file into its own links/ tree, and that is the path
// the library hands back — so the directory beside a track is `links/<hash>/`,
// which contains exactly one symlink and never a cover. The folder image lives
// beside the ORIGINAL, which is the whole point of scanning in place: the person
// keeps their own folders and the cover they put there.
//
// Embedded art needs none of this — opening the symlink reads the real file —
// which is why only the folder half resolves.
func resolve(path string) string {
	if real, err := filepath.EvalSymlinks(path); err == nil {
		return real
	}
	return path
}

// embedded pulls the picture frame out of the audio file's tags.
func embedded(path string) image.Image {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	m, err := tag.ReadFrom(f)
	if err != nil {
		return nil
	}
	pic := m.Picture()
	if pic == nil || len(pic.Data) == 0 {
		return nil
	}
	img, _, err := image.Decode(bytes.NewReader(pic.Data))
	if err != nil {
		return nil
	}
	return img
}

// folderCover names the cover file beside an album, or "" when there is none.
// The directory is listed at most once per cache.
func (c *Cache) folderCover(dir string) string {
	c.mu.Lock()
	if found, ok := c.dirs[dir]; ok {
		c.mu.Unlock()
		return found
	}
	c.mu.Unlock()

	found := scanForCover(dir)

	c.mu.Lock()
	c.dirs[dir] = found
	c.mu.Unlock()
	return found
}

// scanForCover picks the best-named image in a directory.
//
// Names are ranked rather than merely matched, so a folder holding both
// "cover.jpg" and "back.jpg" does not depend on readdir order — and a lone image
// of any name is taken as a last resort, because a scanned sleeve saved as
// "IMG_2231.jpg" is still that album's cover.
func scanForCover(dir string) string {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	best, bestRank := "", len(coverNames)+1
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		ext := strings.ToLower(filepath.Ext(name))
		if !coverExts[ext] {
			continue
		}
		rank := len(coverNames) // any image at all
		stem := strings.ToLower(strings.TrimSuffix(name, filepath.Ext(name)))
		for i, want := range coverNames {
			if stem == want || strings.HasPrefix(stem, want) {
				rank = i
				break
			}
		}
		if rank < bestRank {
			best, bestRank = filepath.Join(dir, name), rank
		}
	}
	return best
}

func decodeFile(path string) image.Image {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		return nil
	}
	return img
}

// shrink scales a cover down to MaxDimension, keeping its aspect ratio. An image
// already small enough is returned untouched — re-encoding it would cost quality
// for nothing.
func shrink(src image.Image) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return nil
	}
	if w <= MaxDimension && h <= MaxDimension {
		return src
	}
	scale := float64(MaxDimension) / float64(w)
	if h > w {
		scale = float64(MaxDimension) / float64(h)
	}
	dst := image.NewRGBA(image.Rect(0, 0, int(float64(w)*scale), int(float64(h)*scale)))
	// CatmullRom rather than a box filter: a cover shrunk 10× with nearest
	// neighbour looks like a JPEG artefact, and this happens once per album.
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, b, draw.Over, nil)
	return dst
}
