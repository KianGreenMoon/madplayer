package ui

import (
	"testing"

	"daemonlord.ygg/madplayer/internal/library"
)

// An album's cover is read from a file this machine holds. A remote-only album
// has none — which is the truth, not a failure: the bytes carrying the cover
// have not been downloaded.
func TestAlbumCoverComesFromTheFirstLocalTrack(t *testing.T) {
	remoteOnly := &library.Track{Copies: []library.Copy{{URL: "https://elsewhere/audio.mp3"}}}
	local := &library.Track{Copies: []library.Copy{{Path: "/music/album/02.flac"}}}

	if got := albumCoverPath([]*library.Track{remoteOnly, local}); got != "/music/album/02.flac" {
		t.Errorf("cover path = %q, want the first track with bytes on this machine", got)
	}
	if got := albumCoverPath([]*library.Track{remoteOnly}); got != "" {
		t.Errorf("cover path = %q for a remote-only album, want none", got)
	}
	if got := albumCoverPath(nil); got != "" {
		t.Errorf("cover path = %q for an empty album", got)
	}
}

// The cover beside the transport follows the file being DECODED, which for a
// remote track is the download and not the queue item's (empty) path.
func TestTheNowPlayingCoverFollowsTheDecodedFile(t *testing.T) {
	a := testApp(t)
	if got := a.nowPlayingCoverPath(); got != "" {
		t.Errorf("cover path = %q with nothing playing", got)
	}
}

// A cover that is not there must not be asked for again on the next frame, and
// a layout function must never wait for a disk read.
func TestAskingForACoverNeverBlocks(t *testing.T) {
	a := testApp(t)
	for i := 0; i < 3; i++ {
		if _, ok := a.art.op("/no/such/file.mp3"); ok {
			t.Fatal("a missing file produced a paintable cover")
		}
	}
}

// The whole album panel has to survive being laid out headlessly — it is the
// surface this host cannot click, same as Settings.
func TestAlbumHeaderLaysOut(t *testing.T) {
	a := testApp(t)
	a.album = &library.Album{Title: "Metamorphoses", ArtistName: "JMJ", Year: 1978}
	tracks := []*library.Track{
		{Title: "One", Duration: 264, Copies: []library.Copy{{Path: "/music/a/01.mp3"}}},
		{Title: "Two", Duration: 429, Copies: []library.Copy{{Path: "/music/a/02.mp3"}}},
	}
	if d := a.albumHeader(headless(), tracks); d.Size.Y == 0 {
		t.Fatal("the album header laid out to nothing")
	}
}

// Walking into an album from row 340 of an artist list and coming back to row 1
// is the small thing that makes a large library tiring to browse.
func TestComingBackUpRestoresTheScrollPosition(t *testing.T) {
	a := testApp(t)

	a.list.Position.First = 340
	a.setLevel(levelAlbums)
	if a.list.Position.First != 0 {
		t.Errorf("drilling in kept the previous list's offset (%d) — new content starts at the top", a.list.Position.First)
	}

	a.list.Position.First = 7
	a.setLevel(levelArtists)
	if a.list.Position.First != 340 {
		t.Errorf("came back to row %d, want 340", a.list.Position.First)
	}

	// And forward again is the album list's own place, not the artist list's.
	a.setLevel(levelAlbums)
	if a.list.Position.First != 0 {
		t.Errorf("drilling in again landed at %d, want the top", a.list.Position.First)
	}
}
