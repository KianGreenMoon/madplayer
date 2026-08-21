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

// The client's half of the two rules the server states in
// docs/architecture/artist-album-model.md and docs/ui/artists-and-performers.md:
//
//   - the artist of a row is the ALBUM artist, and a row must never show a blank
//     credit just because a file carries no artist tag;
//   - identity and order fold case for every alphabet, not only for ASCII.
//
// Neither rule is decided here — the server decides both. What is decided here
// is the MERGE of several libraries into one list, and it re-implements the
// folding, which is exactly where the two can drift apart.

// Two libraries spelling one Cyrillic name differently are one row, the same way
// "Burial" and "burial" are. Go's strings.ToLower is the Unicode-aware fold; a
// byte-wise key would list the same band twice, once per server.
func TestCyrillicCaseVariantsAreOneArtistRow(t *testing.T) {
	got := mergeArtists([][]*Artist{
		{artist("Кино", 9, dev(1))},
		{artist("кино", 12, srv(41))},
		{artist("КИНО", 3, srv2(7))},
	})
	if len(got) != 1 {
		t.Fatalf("merged into %d rows: %v — case variants of one name are one artist", len(got), names(got))
	}
	if got[0].Name != "Кино" {
		t.Errorf("name = %q, want the device's spelling", got[0].Name)
	}
	if len(got[0].Origins) != 3 {
		t.Errorf("origins = %v, want all three libraries", got[0].Origins)
	}
}

// The merged list is alphabetical case-insensitively, in every alphabet. A name
// spelled with a small first letter belongs among its neighbours — sorting by
// raw code point would put every capitalised Cyrillic name in a block before
// every small-lettered one.
func TestTheMergedListIsAlphabeticalInEveryAlphabet(t *testing.T) {
	got := mergeArtists([][]*Artist{{
		artist("Пикник", 1, dev(1)),
		artist("аукцЫон", 1, dev(2)),
		artist("Кино", 1, dev(3)),
		artist("Ленинград", 1, dev(4)),
		artist("Apparat", 1, dev(5)),
	}})
	want := []string{"Apparat", "аукцЫон", "Кино", "Ленинград", "Пикник"}
	if strings.Join(names(got), "|") != strings.Join(want, "|") {
		t.Errorf("order = %v, want %v", names(got), want)
	}
}

// Albums fold the same way, and the folded key must not swallow the bucket rule:
// "Other" still sorts last even in a list of Cyrillic titles.
func TestCyrillicAlbumsFoldAndTheBucketStillSortsLast(t *testing.T) {
	got := mergeAlbums([][]*Album{
		{{Title: "Звезда по имени Солнце", Origins: []Origin{dev(1)}}},
		{{Title: "звезда по имени солнце", Origins: []Origin{srv(2)}}},
		{{Title: "Other", Origins: []Origin{dev(3)}}},
	})
	if len(got) != 2 {
		t.Fatalf("albums = %d, want 2 (the two spellings are one album)", len(got))
	}
	if got[0].Title != "Звезда по имени Солнце" {
		t.Errorf("first album = %q, want the named one before the bucket", got[0].Title)
	}
	if got[1].Title != "Other" {
		t.Errorf("last album = %q, want the %q bucket last", got[1].Title, "Other")
	}
}

// A file with an album-artist tag and no artist tag reaches this client as a row
// with an empty `artist`. The album is being browsed BY its album artist, so the
// answer is right there — showing nothing instead is the bug a person sees as
// "it went into the unknown".
func TestATrackWithNoArtistTagIsCreditedToTheAlbumArtist(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/madnetwork/tracks" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"tracks": []map[string]any{{
			"title": "Sweet Unrest", "artist": "", "track_number": 1,
			"versions": []map[string]any{{
				"renditions": []map[string]any{{"hash": "h1", "size": 10, "codec": "flac"}},
			}},
		}}})
	}))
	defer srv.Close()

	m := madnetworkSource{base: srv.URL, label: madnetworkName, cl: madshare.New(srv.URL, "tok")}
	tracks, err := m.AlbumTracks(context.Background(), m.origin("Apparat"), "The Devil's Walk")
	if err != nil {
		t.Fatal(err)
	}
	if len(tracks) != 1 {
		t.Fatalf("tracks = %d, want 1", len(tracks))
	}
	if tracks[0].Artist != "Apparat" {
		t.Errorf("artist = %q, want the album artist the album was opened by", tracks[0].Artist)
	}
	if tracks[0].Album != "The Devil's Walk" {
		t.Errorf("album = %q, want the album's own title", tracks[0].Album)
	}
}

// A catalogue row names a CODEC, and a file needs an extension. The two are the
// same word often enough to be tempting and not often enough to be right: a
// 24-bit WAV reports `pcm_s24le`, and a kept file called `.pcm_s24le` is one
// nothing here plays and nothing indexes.
func TestACodecNameBecomesAFileExtension(t *testing.T) {
	for _, tc := range []struct{ codec, want string }{
		{"pcm_s24le", ".wav"},
		{"pcm_s16le", ".wav"},
		{"PCM_F32LE", ".wav"},
		{"vorbis", ".ogg"},
		{"aac", ".m4a"},
		{"alac", ".m4a"},
		{"flac", ".flac"},
		{"mp3", ".mp3"},
		{"opus", ".opus"},
		{"", ""},
	} {
		if got := (Copy{Codec: tc.codec, Network: true, Hash: "h"}).Ext(); got != tc.want {
			t.Errorf("Ext() for codec %q = %q, want %q", tc.codec, got, tc.want)
		}
	}
	// A named file still wins: the codec is only consulted when nothing else
	// names the bytes.
	if got := (Copy{Path: "/music/x.flac", Codec: "pcm_s24le"}).Ext(); got != ".flac" {
		t.Errorf("Ext() = %q for a local path, want .flac", got)
	}
	if got := (Copy{URL: "https://host/files/abc/01 - Song.wav", Codec: "mp3"}).Ext(); got != ".wav" {
		t.Errorf("Ext() = %q for a play URL, want .wav", got)
	}
}
