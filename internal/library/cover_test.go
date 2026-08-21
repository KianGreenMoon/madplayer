package library

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"daemonlord.ygg/madplayer/internal/madshare"
)

// Network covers (covers-federation P1/P2): where an album's art can be
// fetched when no file on this device carries it — a signed-in server's own
// album image, or a madnetwork cover relayed by the home server.

var testCoverHash = strings.Repeat("ab", 32)

// coverServer answers the two cover fetch routes plus the album lists that
// name them, and remembers what was asked.
func coverServer(t *testing.T) (*httptest.Server, *[]string) {
	t.Helper()
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.Path+"?"+r.URL.RawQuery)
		switch {
		case r.URL.Path == "/api/albums":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"id": 7, "artist_id": 3, "title": "Pictured", "artist_name": "Band",
					"track_count": 2, "has_image": true},
				{"id": 8, "artist_id": 3, "title": "Plain", "artist_name": "Band",
					"track_count": 1, "has_image": false},
			})
		case r.URL.Path == "/api/albums/7/image":
			if r.URL.Query().Get("size") == "large" {
				_, _ = w.Write([]byte("library-cover-large"))
				return
			}
			_, _ = w.Write([]byte("library-cover-bytes"))
		case r.URL.Path == "/api/albums/7/image/status":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"has_cover": true, "image_hash": testCoverHash, "variants_ready": true})
		case r.URL.Path == "/api/albums/8/image/status":
			// A legacy cover: present, but with no full-hash key to relay by.
			_ = json.NewEncoder(w).Encode(map[string]any{"has_cover": true, "image_hash": ""})
		case r.URL.Path == "/api/albums/8/image":
			_, _ = w.Write([]byte("library-cover-large"))
		case r.URL.Path == "/api/madnetwork/albums":
			_ = json.NewEncoder(w).Encode(map[string]any{"albums": []map[string]any{
				{"title": "Far Album", "tracks": 3, "cover_hash": testCoverHash, "cover_ext": ".jpg"},
			}})
		case r.URL.Path == "/api/madnetwork/cover/"+testCoverHash:
			// No size = the original (the keep-grade answer); display asks medium.
			if r.URL.Query().Get("size") == "" {
				_, _ = w.Write([]byte("original-cover-bytes"))
				return
			}
			if r.URL.Query().Get("size") != "medium" {
				http.Error(w, "unexpected size", http.StatusBadRequest)
				return
			}
			_, _ = w.Write([]byte("network-cover-bytes"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &asked
}

// A server album with art names a CoverRef, one without stays zero, and the
// ref fetches the medium variant — the size every surface here fits inside.
func TestServerAlbumNamesItsCover(t *testing.T) {
	srv, asked := coverServer(t)
	r := remoteSource{base: srv.URL, label: "home", cl: madshare.New(srv.URL, "tok")}

	albums, err := r.Albums(context.Background(), Origin{Source: srv.URL, ID: 3})
	if err != nil || len(albums) != 2 {
		t.Fatalf("albums = %v err=%v, want 2", albums, err)
	}
	pictured, plain := albums[0], albums[1]
	if pictured.Cover.Zero() || pictured.Cover.AlbumID != 7 || pictured.Cover.Source != srv.URL {
		t.Fatalf("pictured album cover ref = %+v, want album 7 at the server", pictured.Cover)
	}
	if !plain.Cover.Zero() {
		t.Errorf("coverless album carries a ref: %+v", plain.Cover)
	}

	data, err := r.FetchCover(context.Background(), pictured.Cover)
	if err != nil || string(data) != "library-cover-bytes" {
		t.Fatalf("FetchCover = %q err=%v", data, err)
	}
	want := "/api/albums/7/image?size=medium"
	if got := (*asked)[len(*asked)-1]; got != want {
		t.Errorf("fetch asked %q, want %q — medium is the agreed size", got, want)
	}
}

// A madnetwork album's elected cover becomes a hash-shaped ref, fetched
// through the home server's relay.
func TestMadnetworkAlbumNamesItsCover(t *testing.T) {
	srv, _ := coverServer(t)
	m := madnetworkSource{base: srv.URL, label: "madnetwork", cl: madshare.New(srv.URL, "tok")}

	albums, err := m.Albums(context.Background(), Origin{Source: m.ID(), Ref: "Band"})
	if err != nil || len(albums) != 1 {
		t.Fatalf("albums = %v err=%v, want 1", albums, err)
	}
	ref := albums[0].Cover
	if ref.Hash != testCoverHash || ref.Source != m.ID() {
		t.Fatalf("cover ref = %+v, want the elected hash at the madnetwork source", ref)
	}
	data, err := m.FetchCover(context.Background(), ref)
	if err != nil || string(data) != "network-cover-bytes" {
		t.Fatalf("FetchCover = %q err=%v", data, err)
	}
}

// Library.FetchCover dispatches by the ref's source, and a zero ref is an
// answer ("nothing to fetch"), not a lookup.
func TestLibraryFetchCoverDispatch(t *testing.T) {
	srv, _ := coverServer(t)
	l := New(nil)
	l.SetServers([]Server{{Base: srv.URL, Label: "home", Client: madshare.New(srv.URL, "tok")}})

	data, err := l.FetchCover(context.Background(), CoverRef{Source: srv.URL, AlbumID: 7})
	if err != nil || string(data) != "library-cover-bytes" {
		t.Fatalf("FetchCover = %q err=%v", data, err)
	}
	if _, err := l.FetchCover(context.Background(), CoverRef{}); err == nil {
		t.Error("a zero ref fetched something")
	}
	if _, err := l.FetchCover(context.Background(), CoverRef{Source: "http://gone.invalid", AlbumID: 1}); err == nil {
		t.Error("a ref to a signed-out server fetched something")
	}
}

// The merge carries the first named cover, and a source that knows none never
// erases one that did.
func TestMergeKeepsTheFirstCover(t *testing.T) {
	ref := CoverRef{Source: "s", AlbumID: 4}
	merged := mergeAlbums([][]*Album{
		{{Title: "One", TrackCount: 1}},                                          // device: no ref
		{{Title: "One", TrackCount: 1, Cover: ref}},                              // server knows art
		{{Title: "One", TrackCount: 1, Cover: CoverRef{Source: "x", Hash: "h"}}}, // later claim loses
	})
	if len(merged) != 1 || merged[0].Cover != ref {
		t.Fatalf("merged cover = %+v, want the first named ref", merged)
	}
}

// FetchCoverOriginal is the keep-grade fetch: the madnetwork ref's hash gets
// the no-size relay answer, and a server-library ref reaches the original
// through the status endpoint's hash plus the same relay.
func TestFetchCoverOriginal(t *testing.T) {
	srv, _ := coverServer(t)
	ctx := context.Background()

	m := madnetworkSource{base: srv.URL, label: "madnetwork", cl: madshare.New(srv.URL, "tok")}
	data, err := m.FetchCoverOriginal(ctx, CoverRef{Source: m.ID(), Hash: testCoverHash})
	if err != nil || string(data) != "original-cover-bytes" {
		t.Fatalf("madnetwork original = %q err=%v", data, err)
	}

	r := remoteSource{base: srv.URL, label: "home", cl: madshare.New(srv.URL, "tok")}
	data, err = r.FetchCoverOriginal(ctx, CoverRef{Source: srv.URL, AlbumID: 7})
	if err != nil || string(data) != "original-cover-bytes" {
		t.Fatalf("library original = %q err=%v — the status hash should reach the relay", data, err)
	}

	// A cover with no full-hash key falls back to the largest variant.
	data, err = r.FetchCoverOriginal(ctx, CoverRef{Source: srv.URL, AlbumID: 8})
	if err != nil || string(data) != "library-cover-large" {
		t.Fatalf("hashless fallback = %q err=%v, want the large variant", data, err)
	}
}
