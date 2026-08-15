package ui

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

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

// A notice is news, and news goes stale.
//
// Nothing ever cleared it before 2026-08-15 — the only assignment of "" was the
// Undo button — so "6 tracks added to the queue" sat above the now-playing line
// for the rest of the session, costing a line of the player bar to describe
// something that had happened an hour ago.
func TestANoticeLeavesOnItsOwn(t *testing.T) {
	a := testApp(t)

	a.enqueue([]*library.Track{localTrack("one")}, false)
	if a.notice == "" {
		t.Fatal("queueing a track said nothing")
	}
	if d := a.noticeLine(headless()); d.Size.Y == 0 {
		t.Fatal("a fresh notice was not drawn")
	}

	a.noticeAt = time.Now().Add(-noticeLife - time.Second)
	if d := a.noticeLine(headless()); d.Size.Y != 0 {
		t.Error("a stale notice is still taking up the line")
	}
	if a.notice != "" {
		t.Errorf("notice = %q, want it forgotten once it expired", a.notice)
	}
}

// assignsNotice matches an assignment to the field, and not a comparison with it.
var assignsNotice = regexp.MustCompile(`a\.notice\b[^=]*=[^=]`)

// Every path that has something to say has to start the clock, or it is the one
// message that stays forever. The compiler cannot check that, so this walks the
// package for assignments to the field instead: only setNotice may write it.
func TestOnlySetNoticeWritesTheNoticeField(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(src), "\n") {
			trimmed := strings.TrimSpace(line)
			if !assignsNotice.MatchString(trimmed) {
				continue
			}
			// The two legitimate writers: setNotice itself, and the clear.
			if strings.HasPrefix(trimmed, "a.notice, a.noticeAt =") || trimmed == `a.notice = ""` {
				continue
			}
			t.Errorf(`%s:%d writes a.notice directly — use setNotice, or the message never expires:
	%s`, f, i+1, trimmed)
		}
	}
}
