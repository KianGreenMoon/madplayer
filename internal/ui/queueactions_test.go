package ui

import (
	"strings"
	"testing"

	"daemonlord.ygg/madplayer/internal/library"
	"daemonlord.ygg/madplayer/internal/queue"
)

func localTrack(title string) *library.Track {
	return &library.Track{Title: title, Copies: []library.Copy{{Path: "/music/" + title + ".flac"}}}
}

func remoteOnlyTrack(title string) *library.Track {
	return &library.Track{Title: title, Copies: []library.Copy{{URL: "https://elsewhere/" + title}}}
}

func unavailableTrack(title string) *library.Track {
	return &library.Track{Title: title}
}

// Adding must not replace. That is the whole reason this exists: with only
// playFrom, every way of choosing music threw away the queue you had built.
func TestAddingToTheQueueKeepsWhatIsThere(t *testing.T) {
	a := testApp(t)
	a.pl.SetQueue(a.itemsFromTracks([]*library.Track{localTrack("one"), localTrack("two")}), 0)

	a.enqueue([]*library.Track{localTrack("three")}, false)
	if got := a.pl.QueueLen(); got != 3 {
		t.Fatalf("queue length %d after adding one to two, want 3", got)
	}
	items := a.pl.QueueItems()
	if items[2].Title != "three" {
		t.Errorf("added track landed at %q, want the end", items[2].Title)
	}
}

// Play next lands right after what is playing, not at the end.
func TestPlayNextLandsAfterTheCurrentTrack(t *testing.T) {
	a := testApp(t)
	a.pl.SetQueue(a.itemsFromTracks([]*library.Track{localTrack("one"), localTrack("two")}), 0)

	a.enqueue([]*library.Track{localTrack("jumped")}, true)
	items := a.pl.QueueItems()
	if len(items) != 3 || items[1].Title != "jumped" {
		t.Fatalf("queue is %v, want the new track second", titles(items))
	}
}

// A remote track is queueable — it is a download, not a missing track. Only a
// track NOTHING can play is refused.
func TestARemoteTrackIsQueueableAndAnUnreachableOneIsNot(t *testing.T) {
	a := testApp(t)
	a.enqueue([]*library.Track{remoteOnlyTrack("far away")}, false)
	if a.pl.QueueLen() != 1 {
		t.Fatal("a remote track was refused — it is a download, not an absence")
	}

	a.enqueue([]*library.Track{unavailableTrack("gone")}, false)
	if got := a.pl.QueueLen(); got != 1 {
		t.Errorf("queue length %d — a track nothing holds was queued anyway", got)
	}
	if !strings.Contains(a.notice, "not on this device") {
		t.Errorf("notice = %q, want it to say why nothing was added", a.notice)
	}
}

// An album with one unplugged drive in it is the case worth naming: adding
// what can be played and saying nothing about the rest hides the loss.
func TestAPartialAddSaysWhatWasLeftOut(t *testing.T) {
	a := testApp(t)
	a.enqueue([]*library.Track{localTrack("here"), unavailableTrack("gone")}, false)

	if got := a.pl.QueueLen(); got != 1 {
		t.Fatalf("queued %d tracks, want only the playable one", got)
	}
	if !strings.Contains(a.notice, "1 not on this device") {
		t.Errorf("notice = %q, want it to count what was skipped", a.notice)
	}
}

func TestTheEnqueueNoticeReadsAsASentence(t *testing.T) {
	for _, tc := range []struct {
		added, asked int
		next         bool
		want         string
	}{
		{1, 1, false, "1 track added to the queue"},
		{1, 1, true, "1 track playing next"},
		{12, 12, false, "12 tracks added to the queue"},
		{11, 12, false, "11 tracks added to the queue — 1 not on this device right now"},
	} {
		if got := enqueueNotice(tc.added, tc.asked, tc.next); got != tc.want {
			t.Errorf("enqueueNotice(%d, %d, %v) = %q, want %q", tc.added, tc.asked, tc.next, got, tc.want)
		}
	}
}

// Clicking a row still replaces the queue — that contract did not change, and
// adding must not have quietly turned every click into an append.
func TestClickingARowStillReplacesTheQueue(t *testing.T) {
	a := testApp(t)
	a.pl.SetQueue(a.itemsFromTracks([]*library.Track{localTrack("old")}), 0)
	a.playFrom([]*library.Track{localTrack("new")}, 0)

	items := a.pl.QueueItems()
	if len(items) != 1 || items[0].Title != "new" {
		t.Fatalf("queue is %v after clicking a row, want just the clicked view", titles(items))
	}
}

func titles(items []*queue.Item) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.Title
	}
	return out
}
