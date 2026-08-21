package backend_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// What the library makes of a real file's tags, through the real scan.
//
// The rules are the server's (docs/architecture/artist-album-model.md), and this
// package is where they are met from the client's end: the scan of a folder is
// how music gets into this program at all, and it is the last step of keeping a
// network track on this device. Both tests below start from bytes on disk for
// that reason — a rule pinned against a row inserted by hand would not have
// caught a tag reader that dropped the field.

// id3v24 hand-builds an ID3v2.4 tag whose text frames are UTF-8 (encoding byte
// 3), which is the only encoding that carries Cyrillic without a codepage. No
// audio follows: the tag reader reads from the head of the stream, and the
// trailing padding is there so the ID3v1 probe at the end has bytes to read.
func id3v24(frames map[string]string) []byte {
	var body bytes.Buffer
	for id, text := range frames {
		frameBody := append([]byte{0x03}, []byte(text)...)
		body.WriteString(id)
		var sz [4]byte
		// v2.4 frame sizes are syncsafe; every frame here is far below 128
		// bytes, where syncsafe and plain agree.
		binary.BigEndian.PutUint32(sz[:], uint32(len(frameBody)))
		body.Write(sz[:])
		body.Write([]byte{0x00, 0x00})
		body.Write(frameBody)
	}
	size := body.Len()
	out := []byte{'I', 'D', '3', 0x04, 0x00, 0x00,
		byte((size >> 21) & 0x7f), byte((size >> 14) & 0x7f),
		byte((size >> 7) & 0x7f), byte(size & 0x7f)}
	out = append(out, body.Bytes()...)
	return append(out, bytes.Repeat([]byte{0}, 256)...)
}

// taggedFolder writes one .mp3 per tag set and returns the folder.
func taggedFolder(t *testing.T, files ...map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for i, frames := range files {
		name := filepath.Join(dir, string(rune('a'+i))+".mp3")
		if err := os.WriteFile(name, id3v24(frames), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// A file that names its album artist and leaves the artist tag empty is filed
// under that album artist, with its album's own title. This is the shape a kept
// track arrives in, and landing in the Unknown-artist bucket under an album
// called "Other" is what it must never do.
func TestAnAlbumArtistTagIsTheArtistOfTheRow(t *testing.T) {
	ctx := context.Background()
	music := taggedFolder(t,
		map[string]string{"TIT2": "Sweet Unrest", "TPE2": "Apparat", "TALB": "The Devil's Walk"},
		map[string]string{"TIT2": "Song of Los", "TPE2": "Apparat", "TALB": "The Devil's Walk"},
	)
	be := open(t, t.TempDir())
	if _, err := be.AddFolder(ctx, music); err != nil {
		t.Fatalf("AddFolder: %v", err)
	}
	be.WaitScan()

	artists, err := be.Library().Artists(ctx)
	if err != nil {
		t.Fatalf("Artists: %v", err)
	}
	if len(artists) != 1 || artists[0].Name != "Apparat" {
		t.Fatalf("artists = %+v, want [Apparat] — an album-artist tag is the artist", artists)
	}
	if artists[0].TrackCount != 2 {
		t.Errorf("track count = %d, want 2", artists[0].TrackCount)
	}

	albums, err := be.Library().AlbumsByArtist(ctx, artists[0].ID)
	if err != nil {
		t.Fatalf("AlbumsByArtist: %v", err)
	}
	if len(albums) != 1 || albums[0].Title != "The Devil's Walk" {
		t.Fatalf("albums = %+v, want the album's own title", albums)
	}
	tracks, err := be.Library().TracksByAlbum(ctx, albums[0].ID)
	if err != nil {
		t.Fatalf("TracksByAlbum: %v", err)
	}
	if len(tracks) != 2 {
		t.Fatalf("tracks = %d, want 2", len(tracks))
	}
	for _, tr := range tracks {
		if tr.ArtistName != "Apparat" {
			t.Errorf("%q is credited to %q, want Apparat — a track with no artist tag "+
				"is performed by its album artist", tr.Title, tr.ArtistName)
		}
	}
}

// Searching this device's library folds case for Cyrillic. The fold happens in
// SQL, where the built-in lower() is ASCII-only — so this is the end-to-end
// statement that the right one is used, in the alphabet it was reported in.
func TestTheDeviceLibrarySearchFoldsCyrillicCase(t *testing.T) {
	ctx := context.Background()
	music := taggedFolder(t, map[string]string{
		"TIT2": "Группа крови", "TPE2": "Кино", "TPE1": "Виктор Цой", "TALB": "Группа крови",
	})
	be := open(t, t.TempDir())
	if _, err := be.AddFolder(ctx, music); err != nil {
		t.Fatalf("AddFolder: %v", err)
	}
	be.WaitScan()

	for _, q := range []string{"кино", "КИНО", "Кино", "кИнО"} {
		res, err := be.Library().Search(ctx, q)
		if err != nil {
			t.Fatalf("Search(%q): %v", q, err)
		}
		if len(res.Artists) != 1 || res.Artists[0].Name != "Кино" {
			t.Errorf("Search(%q) artists = %+v, want [Кино]", q, res.Artists)
		}
	}
	for _, q := range []string{"ГРУППА", "группа кров", "ЦОЙ"} {
		res, err := be.Library().Search(ctx, q)
		if err != nil {
			t.Fatalf("Search(%q): %v", q, err)
		}
		if len(res.Tracks) != 1 {
			t.Errorf("Search(%q) tracks = %d, want 1", q, len(res.Tracks))
		}
	}
}
