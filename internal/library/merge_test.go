package library

import (
	"testing"

	"daemonlord.ygg/madshare/database"
)

func dev(id int64) Origin  { return Origin{Source: DeviceID, Label: DeviceLabel, ID: id} }
func srv(id int64) Origin  { return Origin{Source: "http://host:3000", Label: "host", ID: id} }
func srv2(id int64) Origin { return Origin{Source: "http://other:3000", Label: "other", ID: id} }
func artist(name string, n int, o Origin) *Artist {
	return &Artist{Name: name, TrackCount: n, Origins: []Origin{o}}
}

func names(rows []*Artist) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Name
	}
	return out
}

func TestArtistsMergeOnNameIgnoringCase(t *testing.T) {
	got := mergeArtists([][]*Artist{
		{artist("Burial", 9, dev(1))},
		{artist("burial", 12, srv(41))},
	})
	if len(got) != 1 {
		t.Fatalf("merged into %d rows: %v", len(got), names(got))
	}
	// The device answers first, so the capitalisation already on screen wins.
	if got[0].Name != "Burial" {
		t.Errorf("name = %q, want the device's spelling", got[0].Name)
	}
	if len(got[0].Origins) != 2 {
		t.Errorf("origins = %v, want both libraries", got[0].Origins)
	}
}

// A count is a LOWER BOUND once two libraries contribute: summing would
// double-count everything held in both, which is what a merged view is full of.
func TestMergedCountsAreALowerBoundNotASum(t *testing.T) {
	got := mergeArtists([][]*Artist{
		{artist("Burial", 9, dev(1))},
		{artist("Burial", 12, srv(41))},
	})
	if got[0].TrackCount != 12 {
		t.Errorf("count = %d, want the largest single library's 12", got[0].TrackCount)
	}
	if !got[0].Approx {
		t.Error("a count merged from two libraries must be flagged approximate")
	}

	// One library, one exact answer — no "+" on a row that came from one place.
	only := mergeArtists([][]*Artist{{artist("Burial", 9, dev(1))}})
	if only[0].TrackCount != 9 || only[0].Approx {
		t.Errorf("single-source row = %d approx=%v, want 9 exact", only[0].TrackCount, only[0].Approx)
	}
}

// The server sorts its Unknown-artist bucket last; a merged list that re-sorts
// must keep doing so, or the two disagree about where it belongs.
func TestUnknownArtistSortsLast(t *testing.T) {
	got := mergeArtists([][]*Artist{
		{artist(database.DefaultArtistName, 3, dev(1)), artist("Zappa", 1, dev(2))},
		{artist("Aphex Twin", 5, srv(7))},
	})
	want := []string{"Aphex Twin", "Zappa", database.DefaultArtistName}
	for i, w := range want {
		if got[i].Name != w {
			t.Fatalf("order = %v, want %v", names(got), want)
		}
	}
}

func TestAlbumsMergeAndOtherSortsLast(t *testing.T) {
	got := mergeAlbums([][]*Album{
		{
			{Title: database.DefaultAlbumTitle, Year: 0, TrackCount: 2, Origins: []Origin{dev(1)}},
			{Title: "Untrue", Year: 2007, TrackCount: 13, Origins: []Origin{dev(2)}},
		},
		{
			{Title: "untrue", Year: 2007, TrackCount: 13, Origins: []Origin{srv(9)}},
			{Title: "Burial", Year: 2006, TrackCount: 10, Origins: []Origin{srv(8)}},
		},
	})
	if len(got) != 3 {
		t.Fatalf("merged into %d albums, want 3", len(got))
	}
	// Year first, then title; the Other bucket after everything real.
	if got[0].Title != "Burial" || got[1].Title != "Untrue" || got[2].Title != database.DefaultAlbumTitle {
		t.Fatalf("order = %q, %q, %q", got[0].Title, got[1].Title, got[2].Title)
	}
	if len(got[1].Origins) != 2 {
		t.Errorf("Untrue origins = %v, want both libraries", got[1].Origins)
	}
}

// A year nobody tagged is 0, and a later source that also does not know must not
// erase one that does.
func TestAMissingYearDoesNotEraseAKnownOne(t *testing.T) {
	got := mergeAlbums([][]*Album{
		{{Title: "Untrue", Year: 0, Origins: []Origin{dev(1)}}},
		{{Title: "Untrue", Year: 2007, Origins: []Origin{srv(2)}}},
	})
	if got[0].Year != 2007 {
		t.Errorf("year = %d, want the one library that knew it", got[0].Year)
	}
}

func track(title string, num int, disc *int, o Origin, path, url string) *Track {
	return &Track{
		Title: title, TrackNumber: num, DiscNumber: disc,
		Copies: []Copy{{Origin: o, Path: path, URL: url}},
	}
}

func TestTracksMergeOnDiscTrackAndTitle(t *testing.T) {
	got := mergeTracks([][]*Track{
		{track("Archangel", 2, nil, dev(11), "/music/archangel.flac", "")},
		{track("archangel", 2, nil, srv(22), "", "http://host:3000/files/abc/archangel.mp3")},
	})
	if len(got) != 1 {
		t.Fatalf("merged into %d tracks, want 1", len(got))
	}
	if len(got[0].Copies) != 2 {
		t.Fatalf("copies = %d, want one per library", len(got[0].Copies))
	}
	// Preferring the local copy is the offline case working, not an
	// optimisation: this track must play with the network unplugged.
	best, ok := got[0].Best()
	if !ok || best.Path == "" {
		t.Errorf("Best() = %+v, want the copy with local bytes", best)
	}
	if got[0].Remote() {
		t.Error("a track this device holds is not remote")
	}
}

// A different track number is a different track, however alike the titles are.
func TestTracksWithDifferentNumbersStaySeparate(t *testing.T) {
	got := mergeTracks([][]*Track{
		{track("Untitled", 1, nil, dev(1), "/a.flac", "")},
		{track("Untitled", 2, nil, srv(2), "", "http://h/files/x/b.mp3")},
	})
	if len(got) != 2 {
		t.Fatalf("merged into %d tracks, want 2", len(got))
	}
}

// Untagged, 0 and N are three distinct discs (docs/architecture/disc-numbering.md),
// and the untagged one sorts last, as (disc_number IS NULL) ASC does in SQL.
func TestDiscsStayDistinctAndUntaggedSortsLast(t *testing.T) {
	zero, one := 0, 1
	got := mergeTracks([][]*Track{{
		track("c", 1, nil, dev(3), "/c.flac", ""),
		track("a", 1, &zero, dev(1), "/a.flac", ""),
		track("b", 1, &one, dev(2), "/b.flac", ""),
	}})
	if len(got) != 3 {
		t.Fatalf("merged into %d tracks, want 3 distinct discs", len(got))
	}
	if got[0].Title != "a" || got[1].Title != "b" || got[2].Title != "c" {
		t.Errorf("order = %q, %q, %q; want disc 0, disc 1, untagged last",
			got[0].Title, got[1].Title, got[2].Title)
	}
}

// A track the device lists but cannot reach — an unplugged drive — still plays
// from a server that has it. That is the merge earning its keep.
func TestADanglingLocalCopyFallsBackToAServer(t *testing.T) {
	got := mergeTracks([][]*Track{
		{track("Archangel", 2, nil, dev(11), "", "")}, // listed here, bytes gone
		{track("Archangel", 2, nil, srv(22), "", "http://host:3000/files/abc/a.mp3")},
	})
	if len(got) != 1 {
		t.Fatalf("merged into %d tracks", len(got))
	}
	best, ok := got[0].Best()
	if !ok {
		t.Fatal("track should still be playable from the server")
	}
	if best.URL == "" {
		t.Errorf("Best() = %+v, want the server copy", best)
	}
	if !got[0].Remote() {
		t.Error("with no local bytes, playing this costs a download")
	}
	if got[0].LocalPath() != "" {
		t.Error("LocalPath must be empty when nothing here holds the bytes")
	}
}

func TestThreeLibrariesFoldIntoOneRow(t *testing.T) {
	got := mergeArtists([][]*Artist{
		{artist("Burial", 9, dev(1))},
		{artist("Burial", 3, srv(2))},
		{artist("BURIAL", 4, srv2(3))},
	})
	if len(got) != 1 || len(got[0].Origins) != 3 {
		t.Fatalf("got %d rows with %d origins", len(got), len(got[0].Origins))
	}
	if got[0].TrackCount != 9 {
		t.Errorf("count = %d, want the largest, 9", got[0].TrackCount)
	}
}

func TestEmptyInputMergesToEmpty(t *testing.T) {
	if got := mergeArtists(nil); len(got) != 0 {
		t.Errorf("mergeArtists(nil) = %v", got)
	}
	if got := mergeTracks([][]*Track{{}, {}}); len(got) != 0 {
		t.Errorf("mergeTracks of empty lists = %v", got)
	}
}

func TestHashFromPlayURL(t *testing.T) {
	cases := map[string]string{
		"/files/abc123/song.mp3":                    "abc123",
		"files/abc123/song%20two.mp3":               "abc123",
		"http://host:3000/api/madnetwork/stream/xy": "",
		"": "",
	}
	for in, want := range cases {
		if got := hashFromPlayURL(in); got != want {
			t.Errorf("hashFromPlayURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFileNameGivesTheDecoderItsExtension(t *testing.T) {
	cases := map[string]string{
		"http://host:3000/files/abc/song.mp3": "song.mp3",
		"/files/abc/track.flac?x=1":           "track.flac",
		"song.ogg":                            "song.ogg",
	}
	for in, want := range cases {
		if got := FileName(in); got != want {
			t.Errorf("FileName(%q) = %q, want %q", in, got, want)
		}
	}
}
