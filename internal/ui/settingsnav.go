package ui

import (
	"fmt"

	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget"
	"gioui.org/widget/material"
)

// Settings is an index of pages, not one long scroll.
//
// It grew section by section — folders, appearance, the madnetwork, pairing,
// kept music, the keyboard list, About — until finding the madnetwork switch on
// a phone meant scrolling past two paragraphs about music folders and four
// theme chips, and the page whose title you wanted was three screens down with
// nothing to aim at. The 2026-08-15 fix made that scroll REACHABLE; it could not
// make it navigable.
//
// So the sections became pages behind an index, which is what every settings
// screen on a phone is. Two things fall out of it and both are worth more than
// the tap they cost:
//
//   - The index says what each page currently ANSWERS — how many folders, which
//     theme, whether the madnetwork is on. Most visits to a settings screen are
//     to check something rather than change it, and those end at the index.
//   - A page only lays out when it is open, so the pairing section stops polling
//     the peer table while somebody is choosing a theme.
//
// The table below is the whole structure: the index is generated from it and so
// is the body, so a page that exists and cannot be reached is not expressible,
// and neither is a row that opens nothing.

// settingsPage is which page of Settings is showing.
type settingsPage int

const (
	// pageIndex is the list itself, and the zero value: Settings opens on the
	// index the first time, like any other settings screen.
	pageIndex settingsPage = iota
	pageFolders
	pageAppearance
	pageNetwork
	pagePairing
	pageKeep
	pageKeyboard
	pageDebug
	pageAbout
)

// settingsSection is one page and its row on the index.
type settingsSection struct {
	page  settingsPage
	title string
	// state is the line under the title on the index: what this page says right
	// now. It reads what is ALREADY IN MEMORY and never asks the database — an
	// index is laid out sixty times a second, and the rule about browse rows
	// applies here for the same reason.
	state func(*App) string
	// rows is the page's content, as the items its list scrolls. Most pages are
	// a single item; the folders page is one per folder, because a list inside a
	// list eats the outer one's scroll.
	//
	// It takes the frame's context because a page handles its own buttons, and
	// it is called before anything is laid out — so a click and the frame it
	// changes are the same frame, as everywhere else in this program.
	rows func(*App, C) []layout.Widget
	// hidden takes a page out of the program entirely — not merely off the
	// index, or the pairing experiment's switch would leave a page reachable by
	// a stale a.settingsPage.
	hidden func(*App) bool
}

var settingsSections = []settingsSection{
	{page: pageFolders, title: "Music folders", state: (*App).folderState, rows: (*App).folderRows},
	{page: pageAppearance, title: "Appearance", state: (*App).themeState, rows: onePage((*App).appearanceControls)},
	{page: pageNetwork, title: "The madnetwork", state: (*App).networkState, rows: onePage((*App).networkControls)},
	{page: pagePairing, title: "Node pairing (test)", state: (*App).pairingSummary,
		rows: onePage((*App).pairingControls), hidden: func(*App) bool { return !pairingEnabled }},
	{page: pageKeep, title: "Music kept from the network", state: (*App).keepState, rows: onePage((*App).keepControls)},
	{page: pageKeyboard, title: "Keyboard", state: (*App).keyboardState, rows: onePage((*App).shortcutHelp)},
	{page: pageDebug, title: "Debugging", state: (*App).debugState, rows: (*App).debugRows},
	{page: pageAbout, title: "About madplayer", state: (*App).aboutState, rows: onePage((*App).aboutControls)},
}

// onePage is a section that is a single list item, which is most of them.
func onePage(w func(*App, C) D) func(*App, C) []layout.Widget {
	return func(a *App, _ C) []layout.Widget {
		return []layout.Widget{func(gtx C) D { return w(a, gtx) }}
	}
}

// settings draws whichever page is open. It is the whole panel's entry point.
func (a *App) settings(gtx C) D {
	if sec, ok := a.openSection(); ok {
		return a.settingsPageBody(gtx, sec)
	}
	return a.settingsIndex(gtx)
}

// openSection is the page a.settingsPage names, if it is one that exists in this
// build. Anything else — the index, or a page switched out from under a
// remembered value — falls back to the index.
func (a *App) openSection() (settingsSection, bool) {
	for _, s := range settingsSections {
		if s.page != a.settingsPage {
			continue
		}
		if s.hidden != nil && s.hidden(a) {
			return settingsSection{}, false
		}
		return s, true
	}
	return settingsSection{}, false
}

// openSettingsPage moves within Settings and puts the new page at its top.
//
// The scroll position is shared by every page — one list, as it has been since
// the panel became scrollable — so without the reset a short page opens halfway
// down because a long one was left there.
func (a *App) openSettingsPage(p settingsPage) {
	a.settingsPage = p
	a.folderList.Position = layout.Position{}
}

// settingsIndex is the list of pages.
func (a *App) settingsIndex(gtx C) D {
	shown := make([]settingsSection, 0, len(settingsSections))
	for _, s := range settingsSections {
		if s.hidden != nil && s.hidden(a) {
			continue
		}
		shown = append(shown, s)
	}
	for len(a.settingsBtn) < len(shown) {
		a.settingsBtn = append(a.settingsBtn, widget.Clickable{})
	}
	for i, s := range shown {
		if a.settingsBtn[i].Clicked(gtx) {
			a.openSettingsPage(s.page)
		}
	}

	return layout.Inset{Top: 8, Bottom: 16}.Layout(gtx, func(gtx C) D {
		lst := material.List(a.th, &a.folderList)
		lst.Indicator.Color = colLine
		return lst.Layout(gtx, len(shown), func(gtx C, i int) D {
			return a.settingsRow(gtx, &a.settingsBtn[i], shown[i])
		})
	})
}

// settingsRow is one page on the index: what it is, what it says right now, and
// the chevron that means there is more behind it.
func (a *App) settingsRow(gtx C, click *widget.Clickable, sec settingsSection) D {
	return click.Layout(gtx, func(gtx C) D {
		return layout.Stack{}.Layout(gtx,
			layout.Expanded(func(gtx C) D {
				if click.Hovered() {
					paint.FillShape(gtx.Ops, colSel, clip.Rect{Max: gtx.Constraints.Min}.Op())
				}
				return D{Size: gtx.Constraints.Min}
			}),
			layout.Stacked(func(gtx C) D {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				return layout.Inset{Top: 14, Bottom: 14, Left: 20, Right: 20}.Layout(gtx, func(gtx C) D {
					return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
						layout.Flexed(1, func(gtx C) D {
							return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
								layout.Rigid(func(gtx C) D {
									l := material.Body1(a.th, sec.title)
									l.Color = colFg
									l.MaxLines = 1
									return l.Layout(gtx)
								}),
								layout.Rigid(func(gtx C) D {
									// A page with nothing to report says nothing,
									// rather than reserving a line for a blank.
									state := sec.state(a)
									if state == "" {
										return D{}
									}
									return layout.Inset{Top: 3}.Layout(gtx, func(gtx C) D {
										l := material.Caption(a.th, state)
										l.Color = colDim
										l.MaxLines = 1
										return l.Layout(gtx)
									})
								}),
							)
						}),
						layout.Rigid(func(gtx C) D { return a.chevron(gtx) }),
					)
				})
			}),
		)
	})
}

// settingsPageBody is one open page: the way back, then the section itself,
// which draws its own title and needs no second one above it.
func (a *App) settingsPageBody(gtx C, sec settingsSection) D {
	if a.btnSettingsBack.Clicked(gtx) {
		a.openSettingsPage(pageIndex)
	}
	rows := append([]layout.Widget{a.settingsBack}, sec.rows(a, gtx)...)
	return layout.Inset{Top: 16, Bottom: 16, Left: 20, Right: 20}.Layout(gtx, func(gtx C) D {
		lst := material.List(a.th, &a.folderList)
		lst.Indicator.Color = colLine
		return lst.Layout(gtx, len(rows), func(gtx C, i int) D { return rows[i](gtx) })
	})
}

// settingsBack is the way out of a page. A worded button rather than the browse
// panel's text breadcrumb: this one is pressed with a thumb, and a link the
// height of one line is not something a thumb can reliably hit.
func (a *App) settingsBack(gtx C) D {
	return layout.Inset{Bottom: 6}.Layout(gtx, func(gtx C) D {
		return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx C) D {
				return a.smallButton(gtx, &a.btnSettingsBack, "‹  Settings", false)
			}),
		)
	})
}

// --- what each row says right now -------------------------------------------
//
// Every one of these reads state the program is already holding for its own
// reasons. None of them opens a file, walks a directory or queries the library:
// this is layout code, and it runs sixty times a second.

// folderState counts what is scanned. The track total is the number somebody
// actually wants — "2 folders" says nothing about whether the music is there.
func (a *App) folderState() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.folders) == 0 {
		return "None yet — add one to play the music on this device"
	}
	tracks := 0
	for _, f := range a.folders {
		tracks += f.Tracks
	}
	return plural(len(a.folders), "folder") + " · " + plural(tracks, "track")
}

// themeState names the theme in force, in the words its chip uses.
func (a *App) themeState() string {
	a.mu.Lock()
	name := themeName(a.cfg.Theme)
	a.mu.Unlock()
	for _, t := range themes {
		if t.name == name {
			return t.label
		}
	}
	return ""
}

// networkState is the short form of the madnetwork's status line. The long one
// stays on the page, where there is room for a sentence; an index row has one
// line and it is competing with six other rows for a glance.
func (a *App) networkState() string {
	a.mu.Lock()
	peers := len(a.cfg.MeshPeers)
	wanted := a.cfg.Mesh
	a.mu.Unlock()

	var s string
	_, up := a.be.Mesh()
	switch {
	case a.be.MeshProblem() != "":
		return a.be.MeshProblem()
	case up:
		s = "On"
	case wanted:
		s = "On when madplayer restarts"
	default:
		s = "Off"
	}
	if peers > 0 {
		s += " · " + plural(peers, "peer") + " typed"
	}
	return s
}

// pairingSummary counts friendships, and says nothing at all until the table
// has been read once — a page that has never been opened knows of no peers, and
// "no friends" is a different claim from "not asked yet".
func (a *App) pairingSummary() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.pairing.refreshed.IsZero() {
		return ""
	}
	friends, waiting := 0, 0
	for _, p := range a.pairing.peers {
		switch p.State {
		case "friend":
			friends++
		case "pending_incoming", "pending_outgoing":
			waiting++
		}
	}
	switch {
	case friends == 0 && waiting == 0:
		return "No paired nodes"
	case waiting == 0:
		return plural(friends, "paired node")
	case friends == 0:
		return fmt.Sprintf("%d waiting", waiting)
	}
	return fmt.Sprintf("%s · %d waiting", plural(friends, "paired node"), waiting)
}

// keepState is where kept music goes, and how much has gone there.
func (a *App) keepState() string {
	a.mu.Lock()
	keeper := a.keeper
	a.mu.Unlock()
	if keeper == nil {
		return "Downloads are unavailable in this install"
	}
	s := keeper.Root()
	if kept := keeper.Kept(); kept > 0 {
		s += " · " + plural(kept, "track")
	}
	return s
}

// keyboardState counts the bindings that are worth printing, which is exactly
// the set the page lists — both read the same table, so neither can overcount.
func (a *App) keyboardState() string {
	n := 0
	for _, s := range shortcuts {
		if s.label != "" {
			n++
		}
	}
	return plural(n, "shortcut")
}

// aboutState is what this build IS, which is the whole reason the page exists.
func (a *App) aboutState() string { return a.build.BuildLine() }
