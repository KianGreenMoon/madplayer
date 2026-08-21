package materialize

import (
	"os"
	"path/filepath"
	"testing"
)

// KeepCover (covers-federation P3): the kept album's art lands beside its
// tracks under a name the artwork finder already looks for — fill-if-missing,
// and never where no album folder exists.

func coverKeeper(t *testing.T, technical bool) *Keeper {
	t.Helper()
	k, err := NewKeeper(t.TempDir(), t.TempDir(), true, technical, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestKeepCoverLandsBesideTheAlbum(t *testing.T) {
	k := coverKeeper(t, false)
	tr := Track{Artist: "Band", Album: "First", Title: "Song", Ext: ".mp3"}
	rel, err := tr.Name(false)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(k.Root(), filepath.Dir(rel))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := k.KeepCover(tr, []byte("art"), ".jpg"); err != nil {
		t.Fatalf("KeepCover: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "cover.jpg"))
	if err != nil || string(got) != "art" {
		t.Fatalf("cover.jpg = %q err=%v", got, err)
	}

	// Fill-if-missing: a second cover — even under another extension — never
	// replaces the art already chosen.
	if err := k.KeepCover(tr, []byte("other"), ".png"); err != nil {
		t.Fatalf("second KeepCover: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "cover.png")); !os.IsNotExist(err) {
		t.Error("a second cover was written beside the first")
	}
}

func TestKeepCoverDeclinesQuietly(t *testing.T) {
	tr := Track{Artist: "Band", Album: "First", Title: "Song", Ext: ".mp3", Hash: "abcdef1234"}

	// No album folder: nothing was actually kept there, so nothing to decorate.
	k := coverKeeper(t, false)
	if err := k.KeepCover(tr, []byte("art"), ".jpg"); err != nil {
		t.Fatalf("KeepCover without a folder: %v", err)
	}
	if _, err := os.Stat(filepath.Join(k.Root(), "Band")); !os.IsNotExist(err) {
		t.Error("a cover write invented the album folder")
	}

	// A technical (hash-named) layout has no album folders at all.
	kt := coverKeeper(t, true)
	if err := kt.KeepCover(tr, []byte("art"), ".jpg"); err != nil {
		t.Fatalf("KeepCover on a technical layout: %v", err)
	}
	entries, _ := os.ReadDir(kt.Root())
	if len(entries) != 0 {
		t.Errorf("technical layout gained %d entries from a cover", len(entries))
	}
}
