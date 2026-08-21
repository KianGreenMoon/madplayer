package backend_test

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"daemonlord.ygg/madplayer/internal/materialize"
	"daemonlord.ygg/madplayer/internal/queue"
)

// What a kept track becomes in THIS device's library.
//
// The keeper's own tests use a fake registrar, which is right for what they
// check — where the bytes land and what they are called. This one uses the real
// backend, because the reported bug lives entirely in the half those tests
// stub out: the file was written to the right folder under the right name, and
// the library row it produced said Unknown artist / Other.

// wav builds a RIFF/WAVE file with nothing in it but a format chunk and some
// samples. It carries NO tags of any kind, which is not an odd thing to
// construct: no tag dialect exists for WAV that the reader understands, so every
// WAV in a library is an untagged import — which is exactly what the Pathologic
// 2 OST turned out to be, 47 tracks of it.
func wav(t *testing.T, path string) {
	t.Helper()
	var b []byte
	chunk := func(id string, body []byte) {
		b = append(b, id...)
		var n [4]byte
		binary.LittleEndian.PutUint32(n[:], uint32(len(body)))
		b = append(b, n[:]...)
		b = append(b, body...)
	}
	fmtBody := make([]byte, 16)
	binary.LittleEndian.PutUint16(fmtBody[0:], 1)      // PCM
	binary.LittleEndian.PutUint16(fmtBody[2:], 2)      // channels
	binary.LittleEndian.PutUint32(fmtBody[4:], 48000)  // sample rate
	binary.LittleEndian.PutUint32(fmtBody[8:], 288000) // bytes per second
	binary.LittleEndian.PutUint16(fmtBody[12:], 6)     // block align: 2 channels x 24 bits
	binary.LittleEndian.PutUint16(fmtBody[14:], 24)    // bits
	chunk("fmt ", fmtBody)
	chunk("data", make([]byte, 2048))

	out := append([]byte("RIFF"), 0, 0, 0, 0)
	binary.LittleEndian.PutUint32(out[4:], uint32(4+len(b)))
	out = append(out, "WAVE"...)
	out = append(out, b...)
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatal(err)
	}
}

// fetcher hands back a file that is already on disk, the way a download that has
// landed in the cache does.
type fetcher struct{ path string }

func (f fetcher) Local(context.Context, *queue.Item) (string, error) { return f.path, nil }

// contentHash is what the scanner will compute for these bytes, and therefore
// what the catalogue calls this rendition — the two ARE the same number, which
// is what lets a keep address the row the scan is about to create.
func contentHash(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// A library that knows what a file is must not lose it by copying the bytes.
//
// The tags in the blob are all the scanner can read, and for content pulled off
// the network they are routinely not the whole story — metadata on a madshare is
// an overlay that is never written back into the file, so an album artist set in
// a web UI exists in no blob anywhere. Keeping such a track used to produce a row
// under Unknown artist / Other with a title taken from the filename we had just
// invented, which is the shape the owner reported for the Pathologic 2 OST.
func TestAKeptFileIsDescribedByTheLibraryItCameFrom(t *testing.T) {
	ctx := context.Background()
	be := open(t, t.TempDir())

	source := filepath.Join(t.TempDir(), "blob")
	wav(t, source)

	dataDir, root := t.TempDir(), filepath.Join(t.TempDir(), "madplayer")
	keeper, err := materialize.NewKeeper(dataDir, root, true, false, fetcher{source}, be)
	if err != nil {
		t.Fatalf("NewKeeper: %v", err)
	}

	tr := materialize.Track{
		Artist:    "Pathologic 2", // the album artist: what the album is filed under
		Performer: "Vasily Kashnikov",
		Album:     "Pathologic 2 OST",
		Title:     "Plague Awake Here",
		Number:    1,
		Hash:      contentHash(t, source),
		Ext:       ".wav",
	}
	res, err := keeper.Keep(ctx, tr, &queue.Item{URL: "https://elsewhere/x", Hash: tr.Hash, Title: tr.Title})
	if err != nil {
		t.Fatalf("Keep: %v", err)
	}
	if _, err := os.Stat(res.Path); err != nil {
		t.Fatalf("the file was not written: %v", err)
	}
	be.WaitScan()

	artists, err := be.Library().Artists(ctx)
	if err != nil {
		t.Fatalf("Artists: %v", err)
	}
	if len(artists) != 1 || artists[0].Name != "Pathologic 2" {
		var got []string
		for _, a := range artists {
			got = append(got, a.Name)
		}
		t.Fatalf("artists = %v, want [Pathologic 2] — a kept track keeps the "+
			"identity it was kept FROM, whatever its bytes do or do not say", got)
	}
	albums, err := be.Library().AlbumsByArtist(ctx, artists[0].ID)
	if err != nil {
		t.Fatalf("AlbumsByArtist: %v", err)
	}
	if len(albums) != 1 || albums[0].Title != "Pathologic 2 OST" {
		t.Fatalf("albums = %+v, want [Pathologic 2 OST], not the Other bucket", albums)
	}
	tracks, err := be.Library().TracksByAlbum(ctx, albums[0].ID)
	if err != nil {
		t.Fatalf("TracksByAlbum: %v", err)
	}
	if len(tracks) != 1 {
		t.Fatalf("tracks = %d, want 1", len(tracks))
	}
	if tracks[0].Title != "Plague Awake Here" {
		t.Errorf("title = %q, want the catalogue's title rather than the filename we wrote", tracks[0].Title)
	}
	if tracks[0].ArtistName != "Vasily Kashnikov" {
		t.Errorf("performer = %q, want Vasily Kashnikov", tracks[0].ArtistName)
	}
	if tracks[0].TrackNumber.Int64 != 1 {
		t.Errorf("track number = %v, want 1", tracks[0].TrackNumber)
	}
}

// The file's own tags still win. A keep fills in what the bytes do not say; it
// does not overwrite them with another library's opinion — the same posture the
// server takes, from the other direction.
func TestAKeptFileKeepsTheTagsItAlreadyCarries(t *testing.T) {
	ctx := context.Background()
	be := open(t, t.TempDir())

	source := filepath.Join(t.TempDir(), "blob.flac")
	if err := os.WriteFile(source, id3v24(map[string]string{
		"TIT2": "Darkness", "TPE1": "Theodor Bastard", "TALB": "Utopia",
	}), 0o644); err != nil {
		t.Fatal(err)
	}

	dataDir, root := t.TempDir(), filepath.Join(t.TempDir(), "madplayer")
	keeper, err := materialize.NewKeeper(dataDir, root, true, false, fetcher{source}, be)
	if err != nil {
		t.Fatalf("NewKeeper: %v", err)
	}
	tr := materialize.Track{
		Artist: "Pathologic 2", Performer: "Somebody Else",
		Album: "Some Other Album", Title: "Some Other Title",
		Number: 9, Hash: contentHash(t, source), Ext: ".mp3",
	}
	if _, err := keeper.Keep(ctx, tr, &queue.Item{URL: "https://elsewhere/y", Hash: tr.Hash}); err != nil {
		t.Fatalf("Keep: %v", err)
	}
	be.WaitScan()

	artists, err := be.Library().Artists(ctx)
	if err != nil {
		t.Fatalf("Artists: %v", err)
	}
	// The album artist was the one thing the file did not carry, so it is the
	// one thing the keep supplied — and it is what groups the album.
	if len(artists) != 1 || artists[0].Name != "Pathologic 2" {
		t.Fatalf("artists = %+v, want [Pathologic 2] from the album artist the file lacks", artists)
	}
	albums, _ := be.Library().AlbumsByArtist(ctx, artists[0].ID)
	if len(albums) != 1 || albums[0].Title != "Utopia" {
		t.Fatalf("albums = %+v, want the file's own album title", albums)
	}
	tracks, _ := be.Library().TracksByAlbum(ctx, albums[0].ID)
	if len(tracks) != 1 || tracks[0].Title != "Darkness" || tracks[0].ArtistName != "Theodor Bastard" {
		t.Errorf("track = %+v, want the file's own title and performer", tracks[0])
	}
}
