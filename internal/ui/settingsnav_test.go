package ui

import (
	"image"
	"testing"

	"gioui.org/layout"
)

// Settings is a table of pages now, and the table is load-bearing three ways: it
// generates the index, it dispatches the body, and it decides what exists. So
// what these test is that no page can be listed and not open, opened and not
// lay out, or entered and not left.

// Every page has to survive being opened on a device with nothing configured —
// no folders, no servers, no mesh, no keeper. That is the install where the nils
// are, and it is exactly the state a person is in the first time they go looking
// through Settings.
func TestEverySettingsPageLaysOut(t *testing.T) {
	a := testApp(t)
	for _, sec := range settingsSections {
		if sec.hidden != nil && sec.hidden(a) {
			continue
		}
		a.openSettingsPage(sec.page)
		// Twice, like the panel test: the first pass is what a person sees, the
		// second is where a widget that mutated state during layout shows up.
		for i := 0; i < 2; i++ {
			if d := a.settings(headless()); d.Size.X == 0 && d.Size.Y == 0 {
				t.Errorf("%q laid out to nothing", sec.title)
			}
		}
	}
}

// The index is the only way to reach a page, so a row that does not open its
// page is a page that does not exist. Clicked programmatically, since no button
// on this host can be pressed (CLAUDE.md §Gotchas).
func TestEveryIndexRowOpensItsPage(t *testing.T) {
	a := testApp(t)
	for i, sec := range settingsSections {
		if sec.hidden != nil && sec.hidden(a) {
			continue
		}
		a.openSettingsPage(pageIndex)
		a.settings(headless()) // grows settingsBtn and lays the rows out
		if i >= len(a.settingsBtn) {
			t.Fatalf("the index drew %d rows for %d sections", len(a.settingsBtn), len(settingsSections))
		}
		a.settingsBtn[i].Click()
		a.settings(headless())
		if a.settingsPage != sec.page {
			t.Errorf("row %d (%q) opened page %v", i, sec.title, a.settingsPage)
		}
	}
}

// And every page has a way back out, or a phone is stuck on it.
func TestTheBackButtonReturnsToTheIndex(t *testing.T) {
	a := testApp(t)
	a.openSettingsPage(pageNetwork)
	a.settings(headless())

	a.btnSettingsBack.Click()
	a.settings(headless())
	if a.settingsPage != pageIndex {
		t.Fatalf("back from the madnetwork page left us on %v", a.settingsPage)
	}
}

// A page number that names nothing must fall back to the index rather than
// leaving Settings blank — which is what a remembered page would do the day its
// section is switched off or removed.
func TestAnUnknownPageFallsBackToTheIndex(t *testing.T) {
	a := testApp(t)
	a.settingsPage = settingsPage(9999)
	if _, ok := a.openSection(); ok {
		t.Fatal("a page nothing defines was treated as a real one")
	}
	if d := a.settings(headless()); d.Size.Y == 0 {
		t.Error("Settings laid out nothing for an unknown page")
	}
}

// Opening a page has to start it at the top. Every page shares one scroll
// position — it is one list, as it has been since the panel became scrollable —
// so a short page opened after a long one would otherwise start past its end.
func TestOpeningAPageStartsAtItsTop(t *testing.T) {
	a := testApp(t)
	a.openSettingsPage(pageAbout)
	a.folderList.Position.First = 3
	a.folderList.Position.Offset = 120

	a.openSettingsPage(pageAppearance)
	if a.folderList.Position.First != 0 || a.folderList.Position.Offset != 0 {
		t.Errorf("the theme page opened at %+v, not at its top", a.folderList.Position)
	}
}

// The index and the longest page both have to be reachable end to end on a
// window shorter than they are — a small laptop, a tiled half-screen, and every
// phone. This is the 2026-08-15 finding in its new shape: the sections moved
// behind an index, and neither the index nor a page may strand its own bottom.
func TestSettingsScrollsToTheEndOnAShortWindow(t *testing.T) {
	a := testApp(t)
	short := func() layout.Context {
		gtx := headless()
		gtx.Constraints = layout.Exact(image.Pt(1000, 240))
		return gtx
	}

	for _, p := range []settingsPage{pageIndex, pageAbout} {
		a.openSettingsPage(p)
		a.settings(short())
		if !a.folderList.Position.BeforeEnd {
			t.Fatalf("a 240px window fitted page %v whole — this test is no longer testing anything", p)
		}
		a.folderList.Position.First = 1 << 20 // past the end; a list clamps
		a.settings(short())
		if a.folderList.Position.BeforeEnd {
			t.Errorf("page %v cannot be scrolled to its end", p)
		}
		if a.folderList.Position.Count == 0 {
			t.Errorf("the end of page %v laid out nothing", p)
		}
	}
}

// canGoBack decides whether Android closes the app, and escape decides where
// back goes. They are two readings of the same question, written apart, so they
// can drift — and both ways they can drift are bad: a back button that quits
// from inside a settings page, or one that can never quit at all.
func TestBackGoesSomewhereExactlyWhenItSaysItWill(t *testing.T) {
	a := testApp(t)

	// The one state where back belongs to the system: the top of the library,
	// nothing searched, nothing drilled into.
	if a.canGoBack() {
		t.Fatal("a player sitting at the top of its library claims somewhere to go back to — " +
			"on a phone that is an app the back button cannot close")
	}

	// And every state where it does not, each with the place it must land in.
	// "Something changed" would not do: with the settings branch missing, back
	// from a page leaves Settings altogether — a state change, and the wrong one.
	steps := []struct {
		name   string
		enter  func()
		landed func() bool
		want   string
	}{
		{
			name:   "a settings page",
			enter:  func() { a.view = viewSettings; a.openSettingsPage(pageNetwork) },
			landed: func() bool { return a.view == viewSettings && a.settingsPage == pageIndex },
			want:   "the settings index",
		},
		{
			name:   "the settings index",
			enter:  func() { a.view = viewSettings; a.openSettingsPage(pageIndex) },
			landed: func() bool { return a.view == viewBrowse },
			want:   "the library",
		},
		{
			name:   "the queue",
			enter:  func() { a.view = viewQueue },
			landed: func() bool { return a.view == viewBrowse },
			want:   "the library",
		},
		{
			name:   "a search",
			enter:  func() { a.view = viewBrowse; a.search.SetText("nirvana") },
			landed: func() bool { return a.search.Text() == "" && a.view == viewBrowse },
			want:   "the library with the search cleared",
		},
		{
			name:   "an album",
			enter:  func() { a.view = viewBrowse; a.search.SetText(""); underLock(a, func() { a.setLevel(levelTracks) }) },
			landed: func() bool { return a.level == levelAlbums },
			want:   "that artist's albums",
		},
	}
	for _, s := range steps {
		s.enter()
		if !a.canGoBack() {
			t.Errorf("inside %s, back claims to have nowhere to go", s.name)
			continue
		}
		a.escape(headless())
		if !s.landed() {
			t.Errorf("back from %s should reach %s; it left view=%v page=%v level=%v search=%q",
				s.name, s.want, a.view, a.settingsPage, a.level, a.search.Text())
		}
	}
}
