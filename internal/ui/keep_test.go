package ui

import (
	"strings"
	"testing"

	"daemonlord.ygg/madplayer/internal/library"
)

// The three outcomes are three different sentences on purpose. "Already there"
// is not a failure and must not read like one.
func TestTheKeptSentenceSaysWhichOfTheThreeHappened(t *testing.T) {
	for _, tc := range []struct {
		saved, already int
		failed         []string
		want           []string
		notWant        []string
	}{
		{saved: 1, want: []string{"Kept 1 track in /music"}},
		{saved: 12, want: []string{"Kept 12 tracks in /music"}},
		{already: 3, want: []string{"3 tracks already there"}, notWant: []string{"could not"}},
		{saved: 2, already: 1, failed: []string{"Broken"},
			want: []string{"Kept 2 tracks", "1 track already there", "could not keep Broken"}},
		{failed: []string{"A", "B"}, want: []string{"could not keep 2 tracks"}},
		{want: []string{"already on this device"}},
	} {
		got := keptSentence(tc.saved, tc.already, tc.failed, "/music")
		for _, want := range tc.want {
			if !strings.Contains(got, want) {
				t.Errorf("keptSentence(%d,%d,%v) = %q, want it to contain %q", tc.saved, tc.already, tc.failed, got, want)
			}
		}
		for _, no := range tc.notWant {
			if strings.Contains(got, no) {
				t.Errorf("keptSentence(%d,%d,%v) = %q, must not contain %q", tc.saved, tc.already, tc.failed, got, no)
			}
		}
	}
}

// A stray is IGNORED, and the warning has to say what to do instead — otherwise
// it reads as "your file is broken" rather than "this folder is not yours".
func TestTheStrayWarningSaysWhatToDoInstead(t *testing.T) {
	if got := strayWarning(nil); got != "" {
		t.Errorf("warning = %q for no strays", got)
	}
	one := strayWarning([]string{"Somebody Elses.mp3"})
	for _, want := range []string{"Somebody Elses.mp3", "ignored", "a folder you add yourself"} {
		if !strings.Contains(one, want) {
			t.Errorf("warning %q is missing %q", one, want)
		}
	}
	many := strayWarning([]string{"A.mp3", "B.mp3", "C.mp3"})
	if !strings.Contains(many, "A.mp3") || !strings.Contains(many, "2 other") {
		t.Errorf("warning = %q, want the first named and the rest counted", many)
	}
}

// The blurb has to name the folder, say the folder is the program's, and be
// honest when there is nowhere to keep anything at all.
func TestTheKeepBlurbNamesTheFolderAndItsRules(t *testing.T) {
	got := keepBlurb("/home/kian/Musik/madplayer", 0)
	for _, want := range []string{"/home/kian/Musik/madplayer", "Artist/Album/Track", "madplayer's"} {
		if !strings.Contains(got, want) {
			t.Errorf("blurb %q is missing %q", got, want)
		}
	}
	if strings.Contains(got, "kept so far") {
		t.Errorf("blurb %q counts tracks when none are kept", got)
	}
	if !strings.Contains(keepBlurb("/x", 1), "1 track kept so far") {
		t.Error("the blurb does not count a single kept track")
	}
	if !strings.Contains(keepBlurb("", 0), "nowhere to keep") {
		t.Error("the blurb is not honest about downloads being unavailable")
	}
}

// A track already on this device has nothing to keep, and a button saying
// otherwise would be a button that does nothing.
func TestOnlyRemoteTracksAreKeepable(t *testing.T) {
	a := testApp(t)
	if a.keepable(localTrack("mine")) {
		t.Error("a track already on this device was offered as keepable")
	}
	if a.keepable(nil) {
		t.Error("nil was keepable")
	}
	// The remote answer depends on there being a keeper, which depends on the
	// downloader — so this asserts the pairing rather than a bare true.
	a.mu.Lock()
	keeper := a.keeper
	a.mu.Unlock()
	if got := a.keepable(remoteOnlyTrack("far")); got != (keeper != nil) {
		t.Errorf("keepable = %v with keeper != nil = %v", got, keeper != nil)
	}
}

// Where a kept track lands on disk is a question about the ALBUM, not about the
// track: a compilation belongs in one folder, so the album artist names it and
// the per-track performer never does. What a search hit has instead is the
// track's own credit, and that is the whole of the fallback.
func TestAKeptTrackIsFiledUnderItsAlbumArtist(t *testing.T) {
	a := testApp(t)

	track := &library.Track{
		Title: "Halo", Artist: "Beyonce", Album: "Greatest Comp", TrackNumber: 3,
		Copies: []library.Copy{{URL: "https://elsewhere/halo.flac", Hash: "h1"}},
	}

	// Browsing the album: the album artist wins, so every track on the comp
	// lands in one folder instead of twelve.
	tr, _, err := a.keepTrack(track, "Various Artists")
	if err != nil {
		t.Fatalf("keepTrack: %v", err)
	}
	if tr.Artist != "Various Artists" {
		t.Errorf("artist = %q, want the album artist", tr.Artist)
	}
	if tr.Album != "Greatest Comp" || tr.Number != 3 {
		t.Errorf("track = %+v, want the album and number the row carries", tr)
	}

	// A search hit has no album open, so the row's own credit is all there is.
	tr, _, err = a.keepTrack(track, "")
	if err != nil {
		t.Fatalf("keepTrack (no album): %v", err)
	}
	if tr.Artist != "Beyonce" {
		t.Errorf("artist = %q, want the track's own credit when no album is open", tr.Artist)
	}

	// Whitespace is not an album artist. It used to reach the folder namer as a
	// component made of nothing, which is how music ends up in a directory
	// called " ".
	tr, _, err = a.keepTrack(track, "   ")
	if err != nil {
		t.Fatalf("keepTrack (blank album artist): %v", err)
	}
	if tr.Artist != "Beyonce" {
		t.Errorf("artist = %q for a blank album artist, want the track's credit", tr.Artist)
	}
}

// A track the network offers with an album artist and no artist tag must still
// be filed under a name. The row is credited to its album artist by the browse
// (internal/library), so what reaches here is never empty — and if it somehow
// is, the buckets are the server's own, not an invented placeholder.
func TestAKeptTrackWithNoCreditAtAllUsesTheServersBuckets(t *testing.T) {
	a := testApp(t)

	tr, _, err := a.keepTrack(&library.Track{
		Title:  "Sweet Unrest",
		Copies: []library.Copy{{URL: "https://elsewhere/x.flac", Hash: "h2", Codec: "flac"}},
	}, "")
	if err != nil {
		t.Fatalf("keepTrack: %v", err)
	}
	name, err := tr.Name(false)
	if err != nil {
		t.Fatalf("Name: %v", err)
	}
	if !strings.HasPrefix(name, "Unknown artist/Other/") {
		t.Errorf("path = %q, want the server's Unknown artist / Other buckets", name)
	}
}
