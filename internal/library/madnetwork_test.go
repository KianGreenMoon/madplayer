package library

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"daemonlord.ygg/madplayer/internal/madshare"
)

// Browsing the madnetwork through a server, and what a row from it becomes.
//
// The shape under test is the one the live catalogue actually sends — checked
// against madshare.daemonlord.de on 2026-08-15, where the useful case was a
// track only a THIRD node held: browsed through the home server, fetched from
// somebody else entirely.

// catalogServer answers the four madnetwork browse endpoints and counts what was
// asked, so a test can tell "the network was consulted" from "it was not".
type catalogServer struct {
	*httptest.Server
	pages int // artist pages served, for the paging test
}

func newCatalogServer(t *testing.T) *catalogServer {
	t.Helper()
	cs := &catalogServer{}
	cs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch r.URL.Path {
		case "/api/madnetwork/artists":
			cs.pages++
			// Two pages, so the cursor is followed rather than assumed to be one
			// shot. The live endpoint is cursor-only — there is no bare-array
			// form to fall back to.
			if q.Get("cursor") == "" {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"artists":     []map[string]any{{"name": "Kain Vinosec", "albums": 1, "tracks": 1}},
					"next_cursor": "page2",
				})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"artists": []map[string]any{{"name": "Zeal & Ardor", "albums": 1, "tracks": 14}},
			})
		case "/api/madnetwork/albums":
			if q.Get("artist") != "Kain Vinosec" {
				_ = json.NewEncoder(w).Encode(map[string]any{"albums": []any{}})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"albums": []map[string]any{{"title": "Other", "tracks": 1}},
			})
		case "/api/madnetwork/tracks":
			if q.Get("artist") != "Kain Vinosec" || q.Get("album") != "Other" {
				_ = json.NewEncoder(w).Encode(map[string]any{"tracks": []any{}})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"tracks": []map[string]any{
				{
					"title": "Endure Emptiness", "artist": "Kain Vinosec", "duration": 202.5,
					"versions": []map[string]any{{
						"renditions": []map[string]any{
							{"hash": "abc123", "size": 3242783, "codec": "mp3", "duration": 202.5},
						},
						"holders": []map[string]any{{"name": "somebody else", "reachable": true, "key": "k1"}},
					}},
				},
				// A row nobody offered a fetchable rendition for.
				{"title": "Nothing To Fetch", "artist": "Kain Vinosec",
					"versions": []map[string]any{{"renditions": []map[string]any{}}}},
			}})
		case "/api/madnetwork/search":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"artists": []map[string]any{{"name": "Kain Vinosec", "tracks": 1}},
				"albums":  []map[string]any{{"title": "Other", "tracks": 1}},
				"tracks": []map[string]any{
					{"title": "Endure Emptiness", "artist": "Kain Vinosec", "album_title": "Other", "hash": "abc123"},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(cs.Close)
	return cs
}

func networkSource(t *testing.T) (madnetworkSource, *catalogServer) {
	t.Helper()
	cs := newCatalogServer(t)
	return madnetworkSource{base: cs.URL, label: madnetworkName, cl: madshare.New(cs.URL, "tok")}, cs
}

// The artist list is cursor-only, so a client that stops at the first page shows
// a truncated network and never says so.
func TestTheArtistListIsPagedToTheEnd(t *testing.T) {
	m, cs := networkSource(t)

	artists, err := m.Artists(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(artists) != 2 {
		t.Fatalf("got %d artists, want both pages", len(artists))
	}
	if cs.pages != 2 {
		t.Errorf("asked for %d page(s) — the cursor was not followed", cs.pages)
	}
	if artists[0].Name != "Kain Vinosec" || artists[0].TrackCount != 1 {
		t.Errorf("first row = %+v", artists[0])
	}
	// The address is the NAME: a merged catalogue has no id space, and an id
	// here would be one node's, meaningless to the merge.
	if got := artists[0].Origins[0].Ref; got != "Kain Vinosec" {
		t.Errorf("origin ref = %q, want the name it is addressed by", got)
	}
}

// A track row becomes a copy with a hash, a size and a codec — and no URL,
// because there is no address for it but the content itself.
func TestATrackBecomesANetworkCopy(t *testing.T) {
	m, _ := networkSource(t)

	tracks, err := m.AlbumTracks(context.Background(), m.origin("Kain Vinosec"), "Other")
	if err != nil {
		t.Fatal(err)
	}
	if len(tracks) != 1 {
		t.Fatalf("got %d tracks, want the one with a rendition — a row nothing can fetch is not a track", len(tracks))
	}
	tr := tracks[0]
	if tr.Title != "Endure Emptiness" || tr.Album != "Other" || tr.Duration != 202.5 {
		t.Errorf("track = %+v", tr)
	}
	c := tr.Copies[0]
	switch {
	case !c.Network:
		t.Error("the copy is not marked as coming from the mesh, so it would be looked for on the server")
	case c.Hash != "abc123":
		t.Errorf("hash = %q", c.Hash)
	case c.Size != 3242783:
		t.Errorf("size = %d", c.Size)
	case c.URL != "":
		t.Errorf("url = %q, want none — the server has no copy to hand over", c.URL)
	}
	// The extension the decoders pick by comes from the codec, because there is
	// no filename anywhere in a catalogue row.
	if got := c.Ext(); got != ".mp3" {
		t.Errorf("ext = %q, want .mp3", got)
	}
	if !tr.Available() || !tr.Remote() {
		t.Error("a network track must read as available and as a download")
	}
}

// A version this server holds itself comes with a direct play address, and
// taking it is one request instead of a holder lookup and a swarm. It is the
// server's OWN library file, not the cache-through relay.
func TestAVersionTheServerHoldsIsPlayedDirectly(t *testing.T) {
	cs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"tracks": []map[string]any{{
			"title": "Held Here", "artist": "A",
			"versions": []map[string]any{{
				"renditions": []map[string]any{{"hash": "h", "size": 1, "codec": "flac"}},
				"url":        "/files/h/song.flac",
			}},
		}}})
	}))
	t.Cleanup(cs.Close)
	m := madnetworkSource{base: cs.URL, label: madnetworkName, cl: madshare.New(cs.URL, "tok")}

	tracks, err := m.AlbumTracks(context.Background(), m.origin("A"), "Album")
	if err != nil {
		t.Fatal(err)
	}
	c := tracks[0].Copies[0]
	if c.Network {
		t.Error("a version the server holds was marked for the mesh anyway")
	}
	if c.URL != cs.URL+"/files/h/song.flac" {
		t.Errorf("url = %q, want the absolute play address", c.URL)
	}
}

// An album hit has no artist to open it with, so it is dropped rather than drawn
// as a row that does nothing. Artists and tracks still come back.
func TestSearchDropsTheAlbumHitsItCannotOpen(t *testing.T) {
	m, _ := networkSource(t)

	res, err := m.Search(context.Background(), "endure")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Albums) != 0 {
		t.Errorf("kept %d album hit(s) that cannot be drilled into", len(res.Albums))
	}
	if len(res.Artists) != 1 || len(res.Tracks) != 1 {
		t.Fatalf("artists=%d tracks=%d, want one each", len(res.Artists), len(res.Tracks))
	}
	if c := res.Tracks[0].Copies[0]; !c.Network || c.Hash != "abc123" {
		t.Errorf("search hit copy = %+v", c)
	}
}

// The two sources on one server must not share an id, or an origin from one
// would drill into the other.
func TestTheNetworkSourceIsNotTheServersLibrary(t *testing.T) {
	base := "https://madshare.example"
	lib := remoteSource{base: base, label: "home"}
	net := madnetworkSource{base: base, label: madnetworkName}
	if lib.ID() == net.ID() {
		t.Fatal("a server's library and its madnetwork have the same source id")
	}
	// And the base has to be recoverable from the id, because a fetch asks that
	// server who holds a hash.
	if got := net.ID()[:len(base)]; got != base {
		t.Errorf("the base cannot be read back out of %q", net.ID())
	}
}

// "Only local" is the one narrowing, and it must reach BOTH remote kinds: a
// scope that dropped the servers but kept the network would be a worse offline
// mode than none.
func TestOnlyLocalDropsTheServersAndTheNetwork(t *testing.T) {
	l := &Library{device: fakeSource{id: DeviceID, label: DeviceLabel}}
	l.SetServers([]Server{{Base: "https://one.example", Label: "one", Client: madshare.New("https://one.example", "tok")}})

	if got := len(l.sources()); got != 3 {
		t.Fatalf("all-scope sources = %d, want device + library + madnetwork", got)
	}
	l.SetScope(ScopeDevice)
	got := l.sources()
	if len(got) != 1 || got[0].ID() != DeviceID {
		t.Fatalf("device-scope sources = %d, want only this machine", len(got))
	}
	l.SetScope(ScopeAll)
	if len(l.sources()) != 3 {
		t.Error("widening again did not bring the network back")
	}
}
