package library

import (
	"context"
	"testing"

	"daemonlord.ygg/madshare/api"
	"daemonlord.ygg/madshare/database"
	"daemonlord.ygg/madshare/federation"
)

// Browsing the community through this device's OWN node (source_paired.go) —
// the full-node half of what madnetwork_test.go pins for the server-backed
// path. The shape under test is what madshare's facade actually returns
// (api/database types, not this client's DTOs), and the stakes are the fetch
// path's: a paired row's Copy must say "network, addressed by hash, directed
// at the own node" or playing it asks a server that does not exist.

// fakeNode is app.Madnetwork with two artist pages, one album, one track.
type fakeNode struct {
	pages     int
	coverAsks []string
}

func (f *fakeNode) Artists(_ context.Context, _ string, _ int, cursor string) ([]*database.MadnetworkArtist, string, error) {
	f.pages++
	if cursor == "" {
		return []*database.MadnetworkArtist{{Name: "Kain Vinosec", Albums: 1, Tracks: 1}}, "page2", nil
	}
	return []*database.MadnetworkArtist{{Name: "Zeal & Ardor", Albums: 1, Tracks: 14}}, "", nil
}

func (f *fakeNode) AlbumsByArtist(_ context.Context, artist string) ([]*database.MadnetworkAlbum, error) {
	if artist != "Kain Vinosec" {
		return nil, nil
	}
	year := int64(2021)
	return []*database.MadnetworkAlbum{{Title: "Other", Tracks: 1, Year: &year, CoverHash: "c0ffee"}}, nil
}

func (f *fakeNode) TracksByAlbum(_ context.Context, artist, album string) ([]*api.MadnetworkTrack, error) {
	if artist != "Kain Vinosec" || album != "Other" {
		return nil, nil
	}
	n := int64(1)
	return []*api.MadnetworkTrack{
		{
			Title: "Endure Emptiness", Track: &n, Duration: 183, CoverHash: "c0ffee",
			Versions: []api.MadnetworkVersion{{
				Renditions: []federation.CatalogRendition{{Hash: "abc123", Size: 4 << 20, Codec: "MP3"}},
			}},
		},
		// A catalogue row nobody offered a rendition for is not a track anybody
		// can play; it must be dropped, not shown broken.
		{Title: "Phantom", Versions: []api.MadnetworkVersion{{}}},
	}, nil
}

func (f *fakeNode) Search(_ context.Context, q string) (*api.MadnetworkSearchResults, error) {
	return &api.MadnetworkSearchResults{
		Artists: []*database.MadnetworkArtist{{Name: "Kain Vinosec", Tracks: 1}},
		Tracks: []api.MadnetworkSearchTrack{
			{Title: "Endure Emptiness", Artist: "Kain Vinosec", AlbumTitle: "Other", Hash: "abc123"},
		},
	}, nil
}

func (f *fakeNode) Holders(context.Context, string) (int64, []string, error) {
	return 0, nil, nil
}

// Cover records what was asked and answers with bytes naming the request.
func (f *fakeNode) Cover(_ context.Context, hash, size string) ([]byte, error) {
	f.coverAsks = append(f.coverAsks, hash+"@"+size)
	return []byte("cover:" + hash + ":" + size), nil
}

func TestPairedSourceBrowsesTheOwnNode(t *testing.T) {
	node := &fakeNode{}
	lib := New(emptyDevice{})
	lib.SetNode(node)
	ctx := context.Background()

	artists, _, err := lib.Artists(ctx)
	if err != nil {
		t.Fatalf("Artists: %v", err)
	}
	if len(artists) != 2 {
		t.Fatalf("artists = %d, want 2 (both cursor pages)", len(artists))
	}
	if node.pages != 2 {
		t.Errorf("the cursor was followed %d time(s), want 2 pages", node.pages)
	}

	var kain *Artist
	for _, a := range artists {
		if a.Name == "Kain Vinosec" {
			kain = a
		}
	}
	if kain == nil {
		t.Fatal("the paired catalogue's artist is missing from the merge")
	}
	if src := kain.Origins[0].Source; src != NodeSourceBase+MadnetworkMark {
		t.Errorf("origin source = %q — ui.madnetworkBase would not recover %q", src, NodeSourceBase)
	}

	albums, _, err := lib.Albums(ctx, kain)
	if err != nil || len(albums) != 1 {
		t.Fatalf("Albums = %v, %v; want the one album", albums, err)
	}
	tracks, _, err := lib.AlbumTracks(ctx, albums[0])
	if err != nil {
		t.Fatalf("AlbumTracks: %v", err)
	}
	if len(tracks) != 1 {
		t.Fatalf("tracks = %d, want 1 — the rendition-less row must be dropped", len(tracks))
	}
	c := tracks[0].Copies[0]
	if !c.Network || c.Hash != "abc123" || c.Codec != "mp3" || c.Size != 4<<20 {
		t.Errorf("copy = %+v; want a network copy with hash, lowercased codec and size", c)
	}
	if tracks[0].Artist != "Kain Vinosec" {
		t.Errorf("a blank credit did not fall back to the album artist: %q", tracks[0].Artist)
	}

	// The paired view counts as "beyond this device": the Only-local button
	// and the origin badges hang off Remote(), and a node-mode player with no
	// sign-in needs them exactly as much as a signed-in one.
	if !lib.Remote() {
		t.Error("Remote() is false with the paired view merged in — no way to see only this device's files")
	}

	// The paired view is part of the network, so Only-local excludes it.
	lib.SetScope(ScopeDevice)
	if artists, _, _ := lib.Artists(ctx); len(artists) != 0 {
		t.Errorf("ScopeDevice still lists %d community artists", len(artists))
	}
	lib.SetScope(ScopeAll)

	// Switching node mode off takes the whole catalogue out of the merge — and
	// with it the last non-device source, so the scope machinery stands down.
	lib.SetNode(nil)
	if artists, _, _ := lib.Artists(ctx); len(artists) != 0 {
		t.Errorf("SetNode(nil) left %d community artists in the merge", len(artists))
	}
	if lib.Remote() {
		t.Error("Remote() still true after SetNode(nil) with no servers")
	}
}

// emptyDevice is the app.Library of a machine with no music, so the merge's
// only content is the paired source's.
type emptyDevice struct{}

func (emptyDevice) Artists(context.Context) ([]*database.ArtistEntry, error) { return nil, nil }
func (emptyDevice) ArtistsPage(context.Context, string, int) ([]*database.ArtistEntry, string, error) {
	return nil, "", nil
}
func (emptyDevice) AlbumsByArtist(context.Context, int64) ([]*database.AlbumEntry, error) {
	return nil, nil
}
func (emptyDevice) TracksByAlbum(context.Context, int64) ([]*database.TrackEntry, error) {
	return nil, nil
}
func (emptyDevice) Search(context.Context, string) (*database.SearchResults, error) {
	return &database.SearchResults{}, nil
}
func (emptyDevice) Renditions(context.Context, int64) ([]database.DuplicateRendition, error) {
	return nil, nil
}
func (emptyDevice) BlobPath(string) (string, bool) { return "", false }

// A paired row's cover is fetched through the own node's relay: the album
// and its tracks carry the elected hash as a ref the library routes back to
// the node, medium for the screen and the original for a kept album.
func TestPairedRowsCarryCoversFetchedThroughTheNode(t *testing.T) {
	node := &fakeNode{}
	lib := New(emptyDevice{})
	lib.SetNode(node)
	ctx := context.Background()

	artists, _, err := lib.Artists(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var kain *Artist
	for _, a := range artists {
		if a.Name == "Kain Vinosec" {
			kain = a
		}
	}
	if kain == nil {
		t.Fatal("the paired catalogue's artist is missing")
	}
	albums, _, err := lib.Albums(ctx, kain)
	if err != nil {
		t.Fatal(err)
	}
	var al *Album
	for _, a := range albums {
		if a.Title == "Other" {
			al = a
		}
	}
	if al == nil {
		t.Fatalf("no album Other in %d rows", len(albums))
	}
	if al.Cover.Zero() || al.Cover.Hash != "c0ffee" || al.Cover.Source != pairedID() {
		t.Fatalf("album cover ref %+v, want the elected hash on the paired source", al.Cover)
	}
	tracks, _, err := lib.AlbumTracks(ctx, al)
	if err != nil {
		t.Fatal(err)
	}
	if len(tracks) == 0 || tracks[0].Cover.Hash != "c0ffee" {
		t.Fatalf("track cover ref missing: %+v", tracks)
	}

	got, err := lib.FetchCover(ctx, al.Cover)
	if err != nil || string(got) != "cover:c0ffee:medium" {
		t.Fatalf("FetchCover = %q, %v; want the medium crop through the node", got, err)
	}
	got, err = lib.FetchCoverOriginal(ctx, al.Cover)
	if err != nil || string(got) != "cover:c0ffee:" {
		t.Fatalf("FetchCoverOriginal = %q, %v; want the original through the node", got, err)
	}
	if len(node.coverAsks) != 2 {
		t.Fatalf("the node was asked %v, want exactly the two fetches", node.coverAsks)
	}
}
