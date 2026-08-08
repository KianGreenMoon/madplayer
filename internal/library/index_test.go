package library

import (
	"strings"
	"testing"
)

// track is a terse constructor for the fixtures below.
func track(path, title, artist, albumArtist, album string) *Track {
	return &Track{Path: path, Title: title, Artist: artist, AlbumArtist: albumArtist, Album: album}
}

// witcherLibrary is the worked example from docs/ui/artists-and-performers.md,
// which is the cross-client contract this index has to satisfy:
//
//	Piotr Musiał        — has his own releases AND guests on a compilation
//	Marcin Przybyłowicz — performer only, never an album artist
//	The Witcher 3       — the compilation's album artist
func witcherLibrary() *Index {
	var tracks []*Track
	for i := 0; i < 7; i++ {
		tracks = append(tracks, track(
			"/m/w3/musial"+string(rune('a'+i))+".flac", "W3 cue", "Piotr Musiał",
			"The Witcher 3: Wild Hunt", "The Witcher 3 — Blood and Wine"))
	}
	for i := 0; i < 18; i++ {
		tracks = append(tracks, track(
			"/m/w3/przyb"+string(rune('a'+i))+".flac", "W3 theme", "Marcin Przybyłowicz",
			"The Witcher 3: Wild Hunt", "The Witcher 3 — Blood and Wine"))
	}
	tracks = append(tracks,
		track("/m/fp/1.flac", "The City Must Survive", "Piotr Musiał", "Piotr Musiał", "Frostpunk (OST)"),
		track("/m/fpx/1.flac", "Reheat", "Piotr Musiał", "Piotr Musiał", "Frostpunk Expansions (OST)"),
	)
	return Build(tracks)
}

func artistNames(list []*Artist) []string {
	out := make([]string, len(list))
	for i, a := range list {
		out[i] = a.Name
	}
	return out
}

func TestArtistListShowsAlbumArtistsOnly(t *testing.T) {
	ix := witcherLibrary()
	got := artistNames(ix.Artists())

	want := map[string]bool{"The Witcher 3: Wild Hunt": true, "Piotr Musiał": true}
	for _, name := range got {
		if !want[name] {
			t.Errorf("artist list contains %q; a performer who never released "+
				"anything of their own must be a search hit, not a row", name)
		}
		delete(want, name)
	}
	for name := range want {
		t.Errorf("artist list missing %q", name)
	}
}

func TestPerformerBringsGuestAppearancesWithCorrectCount(t *testing.T) {
	ix := witcherLibrary()

	var musial *Artist
	for _, a := range ix.Artists() {
		if a.Name == "Piotr Musiał" {
			musial = a
		}
	}
	if musial == nil {
		t.Fatal("Piotr Musiał is not in the artist list")
	}

	albums := ix.Albums(musial.ID)
	counts := map[string]int{}
	for _, al := range albums {
		counts[al.Title] = al.TrackCount
	}

	if len(albums) != 3 {
		t.Fatalf("albums = %d (%v), want 3 — the two he owns plus the "+
			"compilation he guests on", len(albums), counts)
	}
	// The compilation counts HIS 7 tracks, not all 25. That hybrid count is the
	// contract: it answers "what will I see if I click this".
	if got := counts["The Witcher 3 — Blood and Wine"]; got != 7 {
		t.Errorf("guest album track count = %d, want 7 (his own, not all 25)", got)
	}
	if got := counts["Frostpunk (OST)"]; got != 1 {
		t.Errorf("owned album track count = %d, want 1", got)
	}
}

func TestAlbumOpensWholeRegardlessOfHowItWasReached(t *testing.T) {
	ix := witcherLibrary()

	var comp *Album
	for _, al := range ix.albums {
		if al.Title == "The Witcher 3 — Blood and Wine" {
			comp = al
		}
	}
	if comp == nil {
		t.Fatal("compilation album missing")
	}

	// The row said 7 when reached from the performer; the album still opens
	// whole, because an id names the release and not a slice of it.
	if got := len(ix.AlbumTracks(comp.ID)); got != 25 {
		t.Errorf("album tracks = %d, want all 25", got)
	}
}

func TestSearchFindsAPerformerWhoIsNotABrowseRow(t *testing.T) {
	ix := witcherLibrary()

	res := ix.Search("przyb")
	if len(res.Artists) == 0 {
		t.Fatal("search found no artist for \"przyb\" — a performer-only artist " +
			"must be reachable, or the browse rule becomes missing data")
	}
	found := false
	for _, a := range res.Artists {
		if a.Name == "Marcin Przybyłowicz" {
			found = true
		}
	}
	if !found {
		t.Errorf("search artists = %v, want Marcin Przybyłowicz", artistNames(res.Artists))
	}
}

func TestUnknownBucketsSortLast(t *testing.T) {
	ix := Build([]*Track{
		track("/m/a.mp3", "A", "", "", ""),            // → Unknown artist / Other
		track("/m/b.mp3", "B", "Zoe", "Zoe", "Zed"),   // named, sorts before it
		track("/m/c.mp3", "C", "Abe", "Abe", "Alpha"), // named
	})

	got := artistNames(ix.Artists())
	if len(got) != 3 || got[len(got)-1] != DefaultArtistName {
		t.Errorf("artists = %v, want the Unknown bucket last", got)
	}

	// And "Other" sorts last among one artist's albums.
	var unknown *Artist
	for _, a := range ix.Artists() {
		if a.IsUnknownArtist() {
			unknown = a
		}
	}
	albums := ix.Albums(unknown.ID)
	if len(albums) != 1 || albums[0].Title != DefaultAlbumTitle {
		t.Fatalf("unknown artist's albums = %v", albums)
	}
}

func TestEveryArtistGetsTheirOwnOtherBucket(t *testing.T) {
	ix := Build([]*Track{
		track("/m/a.mp3", "A", "Abe", "Abe", ""),
		track("/m/b.mp3", "B", "Zoe", "Zoe", ""),
	})
	// Two untagged albums under two artists must not unite into one row.
	if len(ix.albums) != 2 {
		t.Errorf("albums = %d, want 2 — each artist has their own %q",
			len(ix.albums), DefaultAlbumTitle)
	}
}

func TestLiterallyTaggedUnknownFoldsIntoTheBucket(t *testing.T) {
	ix := Build([]*Track{
		track("/m/a.mp3", "A", "", "", "x"),
		track("/m/b.mp3", "B", "unknown  ARTIST", "", "x"),
	})
	if len(ix.Artists()) != 1 {
		t.Errorf("artists = %v, want one bucket — a file tagged \"Unknown artist\" "+
			"lands IN the bucket, not beside it", artistNames(ix.Artists()))
	}
}

func TestNormalSingleArtistReleaseCreatesOneEntity(t *testing.T) {
	ix := Build([]*Track{track("/m/a.mp3", "A", "Air", "Air", "Moon Safari")})
	if len(ix.artists) != 1 {
		t.Errorf("artists = %d, want 1 — performer == album artist is the normal "+
			"case and must not split into two entities", len(ix.artists))
	}
	tr := ix.Tracks()[0]
	if tr.ArtistID != tr.AlbumArtistID {
		t.Error("performer and album artist resolved to different entities")
	}
}

func TestTrackTitleFallsBackToFilename(t *testing.T) {
	ix := Build([]*Track{{Path: "/m/Some Song.mp3"}})
	if got := ix.Tracks()[0].DisplayTitle(); got != "Some Song" {
		t.Errorf("title = %q, want %q", got, "Some Song")
	}
}

func TestAlbumOrderIsDiscThenTrackNumber(t *testing.T) {
	d1, d2 := 1, 2
	ix := Build([]*Track{
		{Path: "/m/d2t1.mp3", Album: "X", Artist: "A", AlbumArtist: "A", DiscNumber: &d2, TrackNumber: 1, Title: "d2t1"},
		{Path: "/m/d1t2.mp3", Album: "X", Artist: "A", AlbumArtist: "A", DiscNumber: &d1, TrackNumber: 2, Title: "d1t2"},
		{Path: "/m/d1t1.mp3", Album: "X", Artist: "A", AlbumArtist: "A", DiscNumber: &d1, TrackNumber: 1, Title: "d1t1"},
	})
	tracks := ix.AlbumTracks(ix.albums[0].ID)
	var order []string
	for _, t := range tracks {
		order = append(order, t.Title)
	}
	if strings.Join(order, ",") != "d1t1,d1t2,d2t1" {
		t.Errorf("order = %v, want d1t1,d1t2,d2t1", order)
	}
	if !IsMultiDisc(tracks) {
		t.Error("IsMultiDisc = false for a two-disc album")
	}
}

func TestUntaggedAndZeroAreDistinctDiscs(t *testing.T) {
	zero := 0
	tracks := []*Track{{DiscNumber: nil}, {DiscNumber: &zero}}
	if !IsMultiDisc(tracks) {
		t.Error("untagged and disc 0 folded together; they are distinct discs")
	}
	if KeyOfDisc(nil).Label() != "Disc —" {
		t.Errorf("untagged label = %q", KeyOfDisc(nil).Label())
	}
	if KeyOfDisc(&zero).Label() != "Disc 0" {
		t.Errorf("disc 0 label = %q", KeyOfDisc(&zero).Label())
	}
}

func TestEmptySearchReturnsNothing(t *testing.T) {
	ix := witcherLibrary()
	if res := ix.Search("   "); len(res.Artists)+len(res.Albums)+len(res.Tracks) != 0 {
		t.Error("blank search returned results")
	}
}
