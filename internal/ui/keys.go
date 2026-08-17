package ui

import (
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/widget"
)

// Keyboard control.
//
// A desktop music player is driven from the keyboard more than from its buttons:
// the window is usually behind something else, and Space is the one control
// everybody already knows. Every shortcut here has a visible twin — nothing is
// reachable ONLY by key — so the bindings are a faster path, never a hidden
// feature (they are listed in Settings for the same reason).
//
// Two rules keep them from fighting the rest of the program:
//
//   - **A focused text box wins.** Gio delivers a key.Filter with no Focus tag
//     regardless of what holds focus, so a bare "Space" filter would type a space
//     into the search box AND toggle playback. Every unmodified binding is
//     therefore gated on [App.typing]; the modified ones (Ctrl+…) are not, because
//     no editor claims them.
//   - **Escape is the back button**, and it is the one binding that also works
//     while typing — that is exactly when a person wants out of the box.
//
// Arrow keys without a modifier are seek, not scroll: the list is scrolled with
// the wheel or the scrollbar, and a player's arrows meaning "seek" is the older
// and stronger convention (mpv, VLC, every web player).
const (
	seekStep   = 5.0  // seconds, plain arrow
	seekJump   = 30.0 // seconds, Shift+arrow
	volumeStep = 0.05
)

// shortcut is one binding, for the filter set and the help list alike. Deriving
// the printed list from the same table that installs the filters is what stops
// the help drifting from the program.
type shortcut struct {
	name     key.Name
	required key.Modifiers
	label    string // what to print; empty means "not worth listing"
	does     string
	// modified bindings survive a focused editor, plain ones do not
	whileTyping bool
	apply       func(a *App, gtx C)
}

var shortcuts = []shortcut{
	{name: key.NameSpace, label: "Space", does: "Play / pause", apply: func(a *App, _ C) { a.pl.Toggle() }},
	{name: key.NameRightArrow, label: "→", does: "Forward 5 seconds", apply: func(a *App, _ C) { a.seekBy(seekStep) }},
	{name: key.NameLeftArrow, label: "←", does: "Back 5 seconds", apply: func(a *App, _ C) { a.seekBy(-seekStep) }},
	{name: key.NameRightArrow, required: key.ModShift, label: "Shift+→", does: "Forward 30 seconds", apply: func(a *App, _ C) { a.seekBy(seekJump) }},
	{name: key.NameLeftArrow, required: key.ModShift, label: "Shift+←", does: "Back 30 seconds", apply: func(a *App, _ C) { a.seekBy(-seekJump) }},

	{name: "N", label: "N", does: "Next track", apply: func(a *App, _ C) { a.pl.Next() }},
	{name: "P", label: "P", does: "Previous track", apply: func(a *App, _ C) { a.pl.Prev() }},
	// The Ctrl+arrow twins exist because that is what the streaming clients
	// bound, and they keep working while a search box has focus.
	{name: key.NameRightArrow, required: key.ModCtrl, whileTyping: true, apply: func(a *App, _ C) { a.pl.Next() }},
	{name: key.NameLeftArrow, required: key.ModCtrl, whileTyping: true, apply: func(a *App, _ C) { a.pl.Prev() }},

	{name: key.NameUpArrow, required: key.ModCtrl, whileTyping: true, label: "Ctrl+↑", does: "Volume up", apply: func(a *App, _ C) { a.volumeBy(volumeStep) }},
	{name: key.NameDownArrow, required: key.ModCtrl, whileTyping: true, label: "Ctrl+↓", does: "Volume down", apply: func(a *App, _ C) { a.volumeBy(-volumeStep) }},

	{name: "S", label: "S", does: "Shuffle on / off", apply: func(a *App, _ C) { a.pl.ToggleShuffle() }},
	{name: "R", label: "R", does: "Repeat: off / all / one", apply: func(a *App, _ C) { a.pl.CycleRepeat() }},
	{name: "Q", label: "Q", does: "Show the queue", apply: func(a *App, _ C) { a.view = toggleView(a.view, viewQueue) }},

	{name: "/", label: "/", does: "Search", apply: (*App).focusSearch},
	{name: "F", required: key.ModCtrl, whileTyping: true, label: "Ctrl+F", does: "Search", apply: (*App).focusSearch},

	// Escape is handled by hand: what it does depends on where you are.
	{name: key.NameEscape, whileTyping: true, label: "Esc", does: "Back", apply: (*App).escape},
}

// keyFilters is the filter set, built once. A filter with no Focus tag matches
// whatever holds focus, which is why the typing gate lives in the handler and
// not here.
var keyFilters = func() []event.Filter {
	out := make([]event.Filter, 0, len(shortcuts))
	for _, s := range shortcuts {
		out = append(out, key.Filter{Name: s.name, Required: s.required})
	}
	return out
}()

// keyFiltersBack is the same set plus Android's hardware back button.
//
// Back is deliberately NOT in the shortcuts table: whether it is filtered is a
// decision taken per frame rather than a binding (see canGoBack), and it has no
// keyboard twin to print in the help — a desktop has Escape, which is the same
// act with a key on it. Both sets are built once so choosing between them costs
// nothing sixty times a second.
var keyFiltersBack = append(append([]event.Filter(nil), keyFilters...),
	key.Filter{Name: key.NameBack})

// editors is every text box in the program.
//
// It exists as one list because the keyboard gate below is only as good as its
// completeness: an editor missing from it takes Space, N, P, S, R, Q and / as
// commands while somebody is typing a path into it. That is not hypothetical —
// keepDirEd was added with the managed folder and left out of here, so typing
// "/home/…" into it jumped to search and toggled playback on the way. A test
// walks the struct and fails when a new editor field is not in this list
// (keys_test.go).
func (a *App) editors() []*widget.Editor {
	return []*widget.Editor{
		&a.search, &a.folderEd,
		&a.srvAddr, &a.srvUser, &a.srvPass,
		&a.cacheEd, &a.keepDirEd,
		&a.pairEd, &a.peerEd,
	}
}

// typing reports whether a text box has focus, which is what makes an
// unmodified letter a letter rather than a command.
func (a *App) typing(gtx C) bool {
	for _, ed := range a.editors() {
		if gtx.Focused(ed) {
			return true
		}
	}
	return false
}

// handleKeys runs the bindings for this frame. It is called from update, before
// anything is laid out, so a key and the frame it changes are the same frame.
func (a *App) handleKeys(gtx C) {
	typing := a.typing(gtx)
	filters := keyFilters
	if a.canGoBack() {
		filters = keyFiltersBack
	}
	for {
		ev, ok := gtx.Event(filters...)
		if !ok {
			return
		}
		ke, isKey := ev.(key.Event)
		if !isKey || ke.State != key.Press {
			continue
		}
		// Back is the phone's, and it is Escape: it steps out of wherever you
		// are, including out of a text box, which is why it is not gated on
		// typing either.
		if ke.Name == key.NameBack {
			a.escape(gtx)
			continue
		}
		for _, s := range shortcuts {
			if s.name != ke.Name || s.required != ke.Modifiers {
				continue
			}
			if typing && !s.whileTyping {
				break
			}
			s.apply(a, gtx)
			break
		}
	}
}

// seekBy moves the playhead relative to where it is. A track with no known
// length is one that has not loaded yet, and seeking into nothing would jump to
// zero — so it is left alone.
func (a *App) seekBy(delta float64) {
	elapsed, total := a.pl.Position()
	if total <= 0 {
		return
	}
	a.pl.Seek(elapsed + delta)
}

// volumeBy nudges the fader and moves the on-screen slider with it, so the
// keyboard and the widget can never disagree about the level.
func (a *App) volumeBy(delta float64) {
	v := a.pl.Volume() + delta
	switch {
	case v < 0:
		v = 0
	case v > 1:
		v = 1
	}
	a.pl.SetVolume(v)
	a.vol.Value = float32(v)
}

// focusSearch puts the caret in the search box, from wherever you were.
func (a *App) focusSearch(gtx C) {
	a.view = viewBrowse
	gtx.Execute(key.FocusCmd{Tag: &a.search})
}

// escape steps back one level: out of a settings page first, then out of a
// panel, then up the drill. This is the order the web UI's back handler uses,
// and since 2026-08-18 it is also what a phone's hardware back button does.
func (a *App) escape(gtx C) {
	// Settings is an index of pages now, so the first step back out of one is
	// to the index rather than out of Settings entirely (settingsnav.go).
	if a.view == viewSettings && a.settingsPage != pageIndex {
		a.openSettingsPage(pageIndex)
		gtx.Execute(key.FocusCmd{Tag: nil})
		return
	}
	if a.view != viewBrowse || a.search.Text() != "" {
		a.search.SetText("")
		a.view = viewBrowse
		gtx.Execute(key.FocusCmd{Tag: nil})
		return
	}
	a.drillUp()
}

// canGoBack reports whether escape has anywhere to go.
//
// On the desktop this decides nothing — Escape with nothing to leave is simply
// a no-op. On Android it decides whether the app stays open at all: Gio hands
// the hardware back press to the router and reads "no handler matched" as
// permission to finish the activity (app/os_android.go, onBack). So the Back
// filter is installed per frame, on this answer, and at the top of the library
// the press is deliberately left unclaimed — that is the one place where
// closing the app IS what back means.
//
// Every branch here mirrors one in escape. A test walks the states and fails if
// the two ever disagree, because the ways they can disagree are both bad: a
// back button that quits from inside a settings page, or one that can never
// quit at all.
func (a *App) canGoBack() bool {
	return a.view != viewBrowse || a.search.Text() != "" || a.level != levelArtists
}
