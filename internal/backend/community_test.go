package backend_test

import (
	"context"
	"testing"

	"daemonlord.ygg/madplayer/internal/backend"
)

// The sharing surface, exercised exactly as the picker uses it: scan a folder,
// take the tagset ids off the browse rows, pin, read back, list. The stake is
// the closed default — this backend pins the node default Local, so what these
// calls say IS what the network can see, and a pin that does not round-trip is
// a library that is either stuck closed or, far worse, quietly open.
func TestSharingRoundTrip(t *testing.T) {
	ctx := context.Background()
	music := musicFolder(t, "one.mp3", "two.mp3")
	be := open(t, t.TempDir())
	if _, err := be.AddFolder(ctx, music); err != nil {
		t.Fatalf("AddFolder: %v", err)
	}
	be.WaitScan()

	lib := be.Library()
	artists, err := lib.Artists(ctx)
	if err != nil || len(artists) != 1 {
		t.Fatalf("Artists = %d, %v", len(artists), err)
	}
	albums, err := lib.AlbumsByArtist(ctx, artists[0].ID)
	if err != nil || len(albums) != 1 {
		t.Fatalf("AlbumsByArtist = %d, %v", len(albums), err)
	}
	rows, err := lib.TracksByAlbum(ctx, albums[0].ID)
	if err != nil || len(rows) != 2 {
		t.Fatalf("TracksByAlbum = %d, %v", len(rows), err)
	}
	ids := []int64{rows[0].TagsetID, rows[1].TagsetID}

	// The resting state: everything answers "not shared", nothing is listed.
	scopes, err := be.ShareScopes(ctx, ids)
	if err != nil {
		t.Fatalf("ShareScopes: %v", err)
	}
	for id, s := range scopes {
		if s != backend.ShareNotShared {
			t.Errorf("fresh scan: tagset %d = %v, want not shared", id, s)
		}
	}
	if shared, err := be.Published(ctx); err != nil || len(shared) != 0 {
		t.Fatalf("fresh scan publishes %v (%v), want nothing", shared, err)
	}

	// Pin one track to friends; exactly that track is published, at that reach.
	if err := be.SetShareScope(ctx, ids[:1], backend.ShareFriends); err != nil {
		t.Fatalf("SetShareScope: %v", err)
	}
	scopes, err = be.ShareScopes(ctx, ids)
	if err != nil || scopes[ids[0]] != backend.ShareFriends || scopes[ids[1]] != backend.ShareNotShared {
		t.Fatalf("scopes after pin = %v, %v", scopes, err)
	}
	shared, err := be.Published(ctx)
	if err != nil || len(shared) != 1 {
		t.Fatalf("Published = %v, %v; want the one pinned track", shared, err)
	}
	if shared[0].TagsetID != ids[0] || shared[0].Scope != backend.ShareFriends {
		t.Errorf("published row = %+v", shared[0])
	}
	if shared[0].Title == "" || shared[0].Artist == "" {
		t.Errorf("a published row must be nameable to its owner: %+v", shared[0])
	}

	// Widen to the madnetwork, then stop — the catalog is empty again.
	if err := be.SetShareScope(ctx, ids[:1], backend.ShareMadnetwork); err != nil {
		t.Fatalf("widen: %v", err)
	}
	if shared, _ := be.Published(ctx); len(shared) != 1 || shared[0].Scope != backend.ShareMadnetwork {
		t.Fatalf("widened = %v", shared)
	}
	if err := be.SetShareScope(ctx, ids[:1], backend.ShareNotShared); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if shared, _ := be.Published(ctx); len(shared) != 0 {
		t.Fatalf("after stop, still published: %v", shared)
	}
}

// With no mesh there is no community to browse — and that is learned from the
// one call that hands out the surface, matching app.Madnetwork's contract.
func TestCommunityIsAbsentWithoutTheMesh(t *testing.T) {
	be := open(t, t.TempDir())
	if _, ok := be.Community(); ok {
		t.Error("Community() available with the mesh off")
	}
	if _, _, err := be.CommunityHolders(context.Background(), "abc"); err == nil {
		t.Error("CommunityHolders with the mesh off should say why it cannot answer")
	}
}
