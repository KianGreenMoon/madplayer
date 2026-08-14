package ui

import (
	"strings"
	"testing"

	"gioui.org/io/key"

	"daemonlord.ygg/madplayer/internal/queue"
)

// The shortcut table is read three ways — it installs the filters, it dispatches
// the presses, and it prints the help — so the invariants it has to hold are
// about the table itself.

// Two bindings on the same chord means one of them silently never runs, and
// which one depends on table order. The dispatcher takes the first match.
func TestNoTwoShortcutsShareAChord(t *testing.T) {
	type chord struct {
		name key.Name
		mods key.Modifiers
	}
	seen := map[chord]string{}
	for _, s := range shortcuts {
		c := chord{s.name, s.required}
		if prev, dup := seen[c]; dup {
			t.Errorf("%v+%v is bound twice (%q and %q)", s.required, s.name, prev, s.does)
		}
		seen[c] = s.does
	}
}

// Every binding needs a filter or the event never arrives, and the filter set is
// derived rather than written — this pins that it stays derived.
func TestEveryShortcutIsFiltered(t *testing.T) {
	if len(keyFilters) != len(shortcuts) {
		t.Fatalf("%d shortcuts but %d filters", len(shortcuts), len(keyFilters))
	}
	for i, f := range keyFilters {
		kf, ok := f.(key.Filter)
		if !ok {
			t.Fatalf("filter %d is not a key filter", i)
		}
		if kf.Name != shortcuts[i].name || kf.Required != shortcuts[i].required {
			t.Errorf("filter %d does not match its shortcut", i)
		}
	}
}

// A plain letter or Space that survived a focused editor would type into the
// search box AND act, which is the whole reason the typing gate exists. Only a
// modified chord — or Escape, which is how you leave the box — may be marked.
func TestOnlyModifiedChordsSurviveAFocusedEditor(t *testing.T) {
	for _, s := range shortcuts {
		if !s.whileTyping {
			continue
		}
		if s.required == 0 && s.name != key.NameEscape {
			t.Errorf("%q is unmodified and still fires while typing", s.does)
		}
	}
}

// The help is generated from the table, so it must actually name things. A
// labelled binding with no description would print a bare key.
func TestTheHelpNamesEveryLabelledBinding(t *testing.T) {
	summary := shortcutSummary()
	for _, s := range shortcuts {
		if s.label == "" {
			continue
		}
		if s.does == "" {
			t.Errorf("%q is labelled but says nothing", s.label)
			continue
		}
		if !strings.Contains(summary, s.label) {
			t.Errorf("%q is missing from the printed shortcuts", s.label)
		}
	}
	if summary == "" {
		t.Fatal("the shortcut help is empty")
	}
}

// The title is what a taskbar shows, so "nothing playing" must not read as an
// empty entry, and a playing track must be identifiable without the window.
func TestTheWindowTitleNamesTheTrack(t *testing.T) {
	a := testApp(t)
	if got := a.windowTitle(); got != "madplayer" {
		t.Errorf("idle title = %q, want the program's name", got)
	}

	// The file behind it never opens — the title is written from the queue item's
	// captured text, which is exactly what makes it work for a track whose drive
	// is unplugged.
	a.pl.SetQueue([]*queue.Item{{Title: "Silent Night", Artist: "Nobody", Path: "/nope.flac"}}, 0)
	got := a.windowTitle()
	for _, want := range []string{"Silent Night", "Nobody", "madplayer"} {
		if !strings.Contains(got, want) {
			t.Errorf("title %q does not carry %q", got, want)
		}
	}
}
