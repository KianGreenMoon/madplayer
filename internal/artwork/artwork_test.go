package artwork

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// pngBytes is a solid square, the smallest thing that is really an image.
func pngBytes(t *testing.T, size int, c color.Color) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// id3WithPicture builds a file whose ID3v2.4 tag carries an APIC frame. It is
// not playable audio and does not need to be: the cover is read out of the tag,
// which is exactly the claim being tested.
func id3WithPicture(t *testing.T, pic []byte) []byte {
	t.Helper()

	// APIC body: encoding, MIME, NUL, picture type, description, NUL, data.
	var body bytes.Buffer
	body.WriteByte(0) // ISO-8859-1
	body.WriteString("image/png")
	body.WriteByte(0)
	body.WriteByte(3) // front cover
	body.WriteByte(0) // empty description
	body.Write(pic)

	var frame bytes.Buffer
	frame.WriteString("APIC")
	frame.Write(syncsafe(uint32(body.Len())))
	frame.Write([]byte{0, 0}) // flags
	frame.Write(body.Bytes())

	var out bytes.Buffer
	out.WriteString("ID3")
	out.Write([]byte{4, 0, 0}) // version 2.4, no flags
	out.Write(syncsafe(uint32(frame.Len())))
	out.Write(frame.Bytes())
	return out.Bytes()
}

// syncsafe is ID3's seven-bits-per-byte size encoding.
func syncsafe(n uint32) []byte {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], ((n&0x7f)<<0)|((n&0x3f80)<<1)|((n&0x1fc000)<<2)|((n&0xfe00000)<<3))
	return b[:]
}

// waitFor polls until the cache has settled, so a test never depends on how
// fast a disk is.
func waitFor(t *testing.T, c *Cache, path string) (image.Image, bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		img, settled := c.Get(path)
		if settled {
			return img, true
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("cover for %s never settled", path)
	return nil, false
}

// The file's own tags come first, because on a compilation there is one folder
// and twelve different covers.
func TestEmbeddedArtWins(t *testing.T) {
	dir := t.TempDir()
	song := filepath.Join(dir, "song.mp3")
	writeFile(t, song, id3WithPicture(t, pngBytes(t, 8, color.RGBA{R: 255, A: 255})))
	writeFile(t, filepath.Join(dir, "cover.png"), pngBytes(t, 16, color.RGBA{B: 255, A: 255}))

	img, _ := waitFor(t, New(), song)
	if img == nil {
		t.Fatal("no cover found for a file carrying one in its tags")
	}
	if b := img.Bounds(); b.Dx() != 8 {
		t.Errorf("cover is %dpx wide — the folder image won over the embedded one", b.Dx())
	}
}

// The folder is the fallback for the untagged case, which is most of anybody's
// older collection.
func TestFolderCoverIsTheFallback(t *testing.T) {
	dir := t.TempDir()
	song := filepath.Join(dir, "song.mp3")
	writeFile(t, song, []byte("not really audio"))
	writeFile(t, filepath.Join(dir, "cover.png"), pngBytes(t, 16, color.RGBA{B: 255, A: 255}))

	img, _ := waitFor(t, New(), song)
	if img == nil {
		t.Fatal("a folder cover beside the file was not found")
	}
	if b := img.Bounds(); b.Dx() != 16 {
		t.Errorf("cover is %dpx wide, want the 16px folder image", b.Dx())
	}
}

// The path the library hands back is a symlink in madshare's links/ tree, and
// the folder beside it holds one symlink and never a cover. The cover is beside
// the ORIGINAL, which is the whole point of scanning folders in place.
func TestTheFolderCoverIsLookedForBesideTheORIGINAL(t *testing.T) {
	root := t.TempDir()
	music := filepath.Join(root, "music", "Some Album")
	links := filepath.Join(root, "links", "abc123")
	if err := os.MkdirAll(music, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(links, 0o755); err != nil {
		t.Fatal(err)
	}
	original := filepath.Join(music, "01 - song.mp3")
	writeFile(t, original, []byte("not really audio"))
	writeFile(t, filepath.Join(music, "cover.png"), pngBytes(t, 16, color.RGBA{G: 255, A: 255}))

	link := filepath.Join(links, "01 - song.mp3")
	if err := os.Symlink(original, link); err != nil {
		t.Fatal(err)
	}

	img, _ := waitFor(t, New(), link)
	if img == nil {
		t.Fatal("no cover found through the link — the folder beside the SYMLINK was searched")
	}
	if b := img.Bounds(); b.Dx() != 16 {
		t.Errorf("cover is %dpx wide, want the 16px one beside the original", b.Dx())
	}
}

// "There is no cover" is a real answer and has to be cached like any other, or
// a folder of untagged files re-reads the disk on every frame.
func TestNoCoverSettlesAndStays(t *testing.T) {
	dir := t.TempDir()
	song := filepath.Join(dir, "song.mp3")
	writeFile(t, song, []byte("not really audio"))

	c := New()
	img, _ := waitFor(t, c, song)
	if img != nil {
		t.Fatal("found a cover where there is none")
	}
	if _, settled := c.Get(song); !settled {
		t.Error("a second look went back to unsettled — the answer was not cached")
	}
}

// Names are ranked, not merely matched, so readdir order cannot decide which
// image is the cover.
func TestTheBestNamedImageWins(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "zzz-back.png"), pngBytes(t, 4, color.Black))
	writeFile(t, filepath.Join(dir, "cover.png"), pngBytes(t, 4, color.White))
	if got := filepath.Base(scanForCover(dir)); got != "cover.png" {
		t.Errorf("picked %q, want cover.png", got)
	}
}

// A sleeve scanned as IMG_2231.jpg is still that album's cover.
func TestALoneImageIsTakenAsTheCover(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "IMG_2231.png"), pngBytes(t, 4, color.White))
	if got := filepath.Base(scanForCover(dir)); got != "IMG_2231.png" {
		t.Errorf("picked %q, want the only image in the folder", got)
	}
}

func TestNonImagesAreIgnored(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "cover.txt"), []byte("nope"))
	if got := scanForCover(dir); got != "" {
		t.Errorf("picked %q from a folder with no images", got)
	}
}

// A booklet scan is 3000px square and a thumbnail is 40dp. Keeping the original
// is how a music player comes to hold a gigabyte of pixels.
func TestBigCoversAreShrunkAndSmallOnesAreNot(t *testing.T) {
	big := image.NewRGBA(image.Rect(0, 0, 1000, 800))
	got := shrink(big)
	if b := got.Bounds(); b.Dx() != MaxDimension {
		t.Errorf("shrunk to %dx%d, want a %dpx long edge", b.Dx(), b.Dy(), MaxDimension)
	} else if b.Dy() != 256 {
		t.Errorf("aspect ratio was not kept: %dx%d", b.Dx(), b.Dy())
	}

	small := image.NewRGBA(image.Rect(0, 0, 100, 100))
	if shrink(small) != image.Image(small) {
		t.Error("an already-small cover was re-scaled for nothing")
	}
}

// The cache is bounded, or browsing a large library is a memory leak with a
// picture on it.
func TestTheCacheIsBounded(t *testing.T) {
	dir := t.TempDir()
	c := New()
	for i := 0; i < maxEntries*2; i++ {
		path := filepath.Join(dir, string(rune('a'+i%26))+string(rune('a'+i/26))+".mp3")
		writeFile(t, path, []byte("x"))
		waitFor(t, c, path)
	}
	c.mu.Lock()
	n, order := len(c.entries), len(c.order)
	c.mu.Unlock()
	if n > maxEntries || order > maxEntries {
		t.Errorf("cache holds %d entries (%d in order), bound is %d", n, order, maxEntries)
	}
}

// OnLoad is how the window learns to repaint. A cover that loads and tells
// nobody is a cover that appears on the next unrelated frame, if ever.
func TestOnLoadFires(t *testing.T) {
	dir := t.TempDir()
	song := filepath.Join(dir, "song.mp3")
	writeFile(t, song, []byte("x"))

	var mu sync.Mutex
	calls := 0
	c := New()
	c.OnLoad = func() {
		mu.Lock()
		calls++
		mu.Unlock()
	}
	waitFor(t, c, song)

	mu.Lock()
	defer mu.Unlock()
	if calls == 0 {
		t.Error("a settled cover never asked for a repaint")
	}
}

// The media bus wants a URL, so embedded art — which lives inside an audio file
// and has no path of its own — is written out on demand.
func TestEmbeddedArtIsSpilledToAFile(t *testing.T) {
	dir := t.TempDir()
	spill := filepath.Join(dir, "covers")
	song := filepath.Join(dir, "song.mp3")
	writeFile(t, song, id3WithPicture(t, pngBytes(t, 8, color.RGBA{R: 255, A: 255})))

	c := New()
	c.SpillDir(spill)
	waitFor(t, c, song)

	file := c.File(song)
	if file == "" {
		t.Fatal("embedded art produced no file")
	}
	if fi, err := os.Stat(file); err != nil || fi.Size() == 0 {
		t.Fatalf("spilled cover is not a file: %v", err)
	}
	// Asked twice, written once — this is called every time a track starts.
	if again := c.File(song); again != file {
		t.Errorf("second call returned %q, want the same file %q", again, file)
	}
}

// A folder cover already IS a file. Copying it would be a second copy of the
// same bytes with a worse name.
func TestAFolderCoverIsHandedOverAsItIs(t *testing.T) {
	dir := t.TempDir()
	song := filepath.Join(dir, "song.mp3")
	cover := filepath.Join(dir, "cover.png")
	writeFile(t, song, []byte("not really audio"))
	writeFile(t, cover, pngBytes(t, 16, color.RGBA{B: 255, A: 255}))

	c := New()
	c.SpillDir(filepath.Join(dir, "covers"))
	waitFor(t, c, song)

	if got := c.File(song); got != cover {
		t.Errorf("File = %q, want the folder cover %q", got, cover)
	}
}

// No art, no file — and no spill directory means no file either, which is the
// state before anything asks for one.
func TestNoArtMeansNoFile(t *testing.T) {
	dir := t.TempDir()
	song := filepath.Join(dir, "song.mp3")
	writeFile(t, song, []byte("not really audio"))

	c := New()
	c.SpillDir(filepath.Join(dir, "covers"))
	waitFor(t, c, song)
	if got := c.File(song); got != "" {
		t.Errorf("File = %q for music with no cover", got)
	}

	bare := New()
	waitFor(t, bare, song)
	if got := bare.File(song); got != "" {
		t.Errorf("File = %q with no spill directory set", got)
	}
}
