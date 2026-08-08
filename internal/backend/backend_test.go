package backend_test

import (
	"context"
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"

	"daemonlord.ygg/madplayer/internal/backend"
)

// open starts a backend on a fresh data dir, quietly.
func open(t *testing.T, dataDir string) *backend.Backend {
	t.Helper()
	be, err := backend.Open(context.Background(), dataDir, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(be.Close)
	return be
}

// musicFolder writes files with audio extensions. The bytes are not real audio:
// tag extraction failing is not fatal to a scan, and what is under test here is
// the path from a folder to a browsable library, not the tag reader.
func musicFolder(t *testing.T, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("bytes of "+n), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// The whole point of level 2a in one test: a folder is imported in place and the
// embedded library can be browsed, with nothing served and nobody signed in.
func TestImportFolderThenBrowse(t *testing.T) {
	ctx := context.Background()
	music := musicFolder(t, "one.mp3", "two.flac", "notes.txt")
	be := open(t, t.TempDir())

	if _, err := be.AddFolder(ctx, music); err != nil {
		t.Fatalf("AddFolder: %v", err)
	}
	be.WaitScan()

	folders, err := be.Folders(ctx)
	if err != nil {
		t.Fatalf("Folders: %v", err)
	}
	if len(folders) != 1 {
		t.Fatalf("Folders = %d, want 1", len(folders))
	}
	f := folders[0]
	if f.Status != "active" {
		t.Errorf("status = %q, want active", f.Status)
	}
	if f.Tracks != 2 {
		t.Errorf("linked %d files, want 2 (the .txt is not audio)", f.Tracks)
	}
	if f.Missing {
		t.Error("a folder that is right there should not read as missing")
	}

	// The originals are untouched — the hard invariant an in-place import carries.
	entries, err := os.ReadDir(music)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Errorf("the scanned folder now holds %d entries, want the original 3", len(entries))
	}

	artists, err := be.Library().Artists(ctx)
	if err != nil {
		t.Fatalf("Artists: %v", err)
	}
	if len(artists) != 1 {
		// Untagged files land in the Unknown-artist bucket, which is one row.
		t.Fatalf("Artists = %d rows, want 1", len(artists))
	}
	albums, err := be.Library().AlbumsByArtist(ctx, artists[0].ID)
	if err != nil || len(albums) != 1 {
		t.Fatalf("AlbumsByArtist = (%d rows, %v), want 1 row", len(albums), err)
	}
	tracks, err := be.Library().TracksByAlbum(ctx, albums[0].ID)
	if err != nil {
		t.Fatalf("TracksByAlbum: %v", err)
	}
	if len(tracks) != 2 {
		t.Fatalf("TracksByAlbum = %d rows, want 2", len(tracks))
	}
	for _, tr := range tracks {
		if tr.Title == "" {
			t.Error("a track title is never empty — the server falls back to the filename")
		}
		// Playback is a path, not a URL, and it must resolve through the links
		// storage to the original file.
		path, ok := be.Library().BlobPath(tr.ObjectKey)
		if !ok {
			t.Fatalf("BlobPath(%q) did not resolve", tr.ObjectKey)
		}
		real, err := filepath.EvalSymlinks(path)
		if err != nil {
			t.Fatalf("resolve %s: %v", path, err)
		}
		if filepath.Dir(real) != mustEval(t, music) {
			t.Errorf("track resolves to %s, want a file inside %s", real, music)
		}
	}
}

// A second run provisions nothing and re-reads what the first one imported — the
// case that decides whether the embedded store is actually persistent.
func TestReopenKeepsTheLibrary(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	music := musicFolder(t, "one.mp3")

	first := open(t, dataDir)
	if _, err := first.AddFolder(ctx, music); err != nil {
		t.Fatalf("AddFolder: %v", err)
	}
	first.WaitScan()
	first.Close()

	second := open(t, dataDir)
	folders, err := second.Folders(ctx)
	if err != nil || len(folders) != 1 {
		t.Fatalf("Folders after reopen = (%d, %v), want 1", len(folders), err)
	}
	artists, err := second.Library().Artists(ctx)
	if err != nil || len(artists) != 1 {
		t.Fatalf("Artists after reopen = (%d, %v), want 1", len(artists), err)
	}
}

// An unplugged drive is a state, not an error: the folder still lists, says it is
// not connected, and keeps its tracks.
func TestFolderGoesAway(t *testing.T) {
	ctx := context.Background()
	parent := t.TempDir()
	music := filepath.Join(parent, "sdcard")
	if err := os.Mkdir(music, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(music, "one.mp3"), []byte("bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	be := open(t, t.TempDir())
	if _, err := be.AddFolder(ctx, music); err != nil {
		t.Fatalf("AddFolder: %v", err)
	}
	be.WaitScan()
	if err := os.RemoveAll(music); err != nil {
		t.Fatal(err)
	}

	folders, err := be.Folders(ctx)
	if err != nil || len(folders) != 1 {
		t.Fatalf("Folders = (%d, %v), want the folder to still be listed", len(folders), err)
	}
	if !folders[0].Missing {
		t.Error("a folder that is gone should report Missing")
	}
	if folders[0].Tracks != 1 {
		t.Errorf("Tracks = %d, want the last scan's count kept", folders[0].Tracks)
	}

	// The library still lists the track; only its bytes are unreachable, which is
	// what the UI turns into "not on this device right now".
	artists, err := be.Library().Artists(ctx)
	if err != nil || len(artists) != 1 {
		t.Fatalf("Artists = (%d, %v), want the track to still be listed", len(artists), err)
	}
	albums, _ := be.Library().AlbumsByArtist(ctx, artists[0].ID)
	if len(albums) != 1 {
		t.Fatalf("AlbumsByArtist = %d, want 1", len(albums))
	}
	tracks, _ := be.Library().TracksByAlbum(ctx, albums[0].ID)
	if len(tracks) != 1 {
		t.Fatalf("TracksByAlbum = %d, want 1", len(tracks))
	}
	if _, ok := be.Library().BlobPath(tracks[0].ObjectKey); ok {
		t.Error("BlobPath resolved a file that is gone — a dangling link must fall through")
	}
}

// A typo must be refused with a reason. A silently ignored one looks exactly like
// an empty library.
func TestAddFolderRefusesNonsense(t *testing.T) {
	ctx := context.Background()
	be := open(t, t.TempDir())

	for _, path := range []string{"", filepath.Join(t.TempDir(), "not-there")} {
		if _, err := be.AddFolder(ctx, path); err == nil {
			t.Errorf("AddFolder(%q) should fail", path)
		}
	}
	// A file is not a folder.
	file := filepath.Join(t.TempDir(), "song.mp3")
	if err := os.WriteFile(file, []byte("bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := be.AddFolder(ctx, file); err == nil {
		t.Error("AddFolder(a file) should fail")
	}
}

func mustEval(t *testing.T, path string) string {
	t.Helper()
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return real
}
