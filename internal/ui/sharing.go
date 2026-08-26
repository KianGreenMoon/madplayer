package ui

// Node mode's sharing surface (docs/design.md §"Node mode"; the design is
// madshare's docs/plans/full-node-mode.md P2): the album header's share
// control — where the choice is made, next to the music it is about — and the
// Settings "Sharing" page listing exactly what this device publishes, which is
// the visible half of the responsibility the closed default protects.
//
// Nothing is shared until chosen. The node default stays pinned Local
// (backend.PublishNothing), so every published row is a pin somebody made
// here, and un-pinning on the Sharing page empties the catalog again.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"gioui.org/layout"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"daemonlord.ygg/madplayer/internal/backend"
	"daemonlord.ygg/madplayer/internal/library"
)

// sharingRefresh is how stale the published list may be while its page is on
// screen — same shape as pairingRefresh, same bounded cost.
const sharingRefresh = 5 * time.Second

// sharingState is everything sharing owns beyond the two buttons' clickables.
// Widget fields belong to the UI goroutine; the rest is guarded by App.mu.
type sharingState struct {
	stop []widget.Clickable

	// under App.mu
	rows      []backend.SharedTrack
	refreshed time.Time
	loading   bool
	busy      bool
	msg       string

	// The album whose scopes are loaded for the header's share control, keyed
	// by artist+title+the device tagset ids — two views of one album can hold
	// different id sets (ScopeDevice against ScopeAll, a reissue in a second
	// folder), and scopes loaded for one must not label the other. Reset when
	// the person drills elsewhere; the control shows "Sharing…" until the
	// read lands.
	albumKey     string
	scopes       map[int64]backend.ShareScope
	albumLoading bool
}

// applyNodeMode wires (or unwires) node mode's live pieces from the current
// pref: the paired browse source and the fetcher's holder directory. Called
// from the startup's mesh arm and from the Settings switch, so toggling the
// mode needs no restart — the mesh underneath it does, and the node-mode page
// says so where that matters.
func (a *App) applyNodeMode() {
	var node library.PairedNode
	if a.nodeMode() {
		if mn, ok := a.be.Community(); ok {
			node = mn
		}
	}
	a.lib.SetNode(node)
	if a.fetch != nil {
		if node != nil {
			a.fetch.SetDirectory(a.be)
		} else {
			a.fetch.SetDirectory(nil)
		}
	}
}

// ── The album header's share control ─────────────────────────────────────────

// deviceTagsetIDs is the appearances this album has on THIS device — the only
// rows a share control can act on, and their Origin.ID is the tagset id the
// backend addresses scopes by.
func deviceTagsetIDs(tracks []*library.Track) []int64 {
	var ids []int64
	for _, t := range tracks {
		for _, c := range t.Copies {
			if c.Origin.OnDevice() && c.Origin.ID > 0 {
				ids = append(ids, c.Origin.ID)
			}
		}
	}
	return ids
}

// albumShareKey names one album VIEW: the display identity plus the device
// tagset ids the control would act on. The ids are part of the key because two
// views with the same artist and title can hold different sets, and a scope
// map loaded for one must not be read (or written through) for the other.
func (a *App) albumShareKey(ids []int64) string {
	var b strings.Builder
	b.WriteString(a.album.ArtistName)
	b.WriteByte(0)
	b.WriteString(a.album.Title)
	for _, id := range ids {
		fmt.Fprintf(&b, "\x00%d", id)
	}
	return b.String()
}

// albumShareButton is the header's share control, nil when it does not apply:
// node mode off, or an album this device holds nothing of (sharing is a
// statement about one's own copies). The label IS the state — off, friends,
// madnetwork, or mixed — and a click moves to the next scope in that ring.
func (a *App) albumShareButton(tracks []*library.Track) layout.Widget {
	if !a.nodeMode() {
		return nil
	}
	ids := deviceTagsetIDs(tracks)
	if len(ids) == 0 {
		return nil
	}

	key := a.albumShareKey(ids)
	a.mu.Lock()
	stale := a.sharing.albumKey != key && !a.sharing.albumLoading
	if stale {
		a.sharing.albumLoading = true
	}
	scopes := a.sharing.scopes
	loaded := a.sharing.albumKey == key
	busy := a.sharing.busy
	a.mu.Unlock()
	if stale {
		go a.loadAlbumScopes(key, ids)
	}

	label := "Sharing…"
	if loaded {
		switch scope, mixed := uniformScope(ids, scopes); {
		case mixed:
			label = "Sharing: mixed"
		case scope == backend.ShareFriends:
			label = "Sharing: friends"
		case scope == backend.ShareMadnetwork:
			label = "Sharing: madnetwork"
		default:
			label = "Sharing: off"
		}
	}
	return func(gtx C) D { return a.smallButton(gtx, &a.btnAlbumShare, label, busy || !loaded) }
}

// uniformScope is the album's one scope, or mixed=true when its tracks
// disagree (tracks shared one by one, or an album merged from two states).
func uniformScope(ids []int64, scopes map[int64]backend.ShareScope) (backend.ShareScope, bool) {
	scope := scopes[ids[0]]
	for _, id := range ids[1:] {
		if scopes[id] != scope {
			return scope, true
		}
	}
	return scope, false
}

func (a *App) loadAlbumScopes(key string, ids []int64) {
	scopes, err := a.be.ShareScopes(context.Background(), ids)
	a.mu.Lock()
	a.sharing.albumLoading = false
	if err == nil {
		a.sharing.albumKey, a.sharing.scopes = key, scopes
	}
	a.mu.Unlock()
	a.win.Invalidate()
}

// cycleAlbumShare moves the album to the next scope: off → friends →
// madnetwork → off. Mixed normalizes to friends — the smallest positive
// answer, so a mistaken tap widens nothing.
func (a *App) cycleAlbumShare(tracks []*library.Track) {
	ids := deviceTagsetIDs(tracks)
	if len(ids) == 0 {
		// The button is only offered while the album has device copies, but a
		// click can land a frame after a reload dropped them (a drive ejected —
		// a normal state here), and uniformScope indexes into the ids.
		return
	}
	key := a.albumShareKey(ids)
	a.mu.Lock()
	if a.sharing.busy || a.sharing.albumKey != key {
		a.mu.Unlock()
		return
	}
	a.sharing.busy = true
	scope, mixed := uniformScope(ids, a.sharing.scopes)
	a.mu.Unlock()

	next := backend.ShareNotShared
	switch {
	case mixed:
		next = backend.ShareFriends
	case scope == backend.ShareNotShared:
		next = backend.ShareFriends
	case scope == backend.ShareFriends:
		next = backend.ShareMadnetwork
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		err := a.be.SetShareScope(ctx, ids, next)
		scopes, rerr := a.be.ShareScopes(ctx, ids)
		a.mu.Lock()
		a.sharing.busy = false
		if err == nil && rerr == nil {
			a.sharing.albumKey, a.sharing.scopes = key, scopes
			// The published list is stale now; the page re-reads on its next look.
			a.sharing.refreshed = time.Time{}
		}
		a.mu.Unlock()
		if err != nil {
			a.setNotice("could not change sharing: " + err.Error())
		} else {
			a.setNotice(shareNotice(next, len(ids)))
		}
		a.win.Invalidate()
	}()
}

// shareNotice says what just happened, honestly about reach: a share is a
// statement to real people's nodes, so it names the audience rather than
// saying "done".
func shareNotice(s backend.ShareScope, n int) string {
	switch s {
	case backend.ShareFriends:
		return fmt.Sprintf("Sharing %s with your paired nodes", plural(n, "track"))
	case backend.ShareMadnetwork:
		return fmt.Sprintf("Sharing %s with the whole madnetwork", plural(n, "track"))
	}
	return fmt.Sprintf("Stopped sharing %s", plural(n, "track"))
}

// ── The Sharing page ─────────────────────────────────────────────────────────

// sharingSummary is the index row's line. Silent until the list has been read
// once — "nothing shared" is a claim, "not asked yet" is not.
func (a *App) sharingSummary() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.sharing.refreshed.IsZero() {
		return ""
	}
	if len(a.sharing.rows) == 0 {
		return "Nothing is shared from this device"
	}
	return plural(len(a.sharing.rows), "track") + " shared"
}

// sharingControls is the page: what sharing means here, then every published
// row with the one act that makes sense on a listing — stop.
func (a *App) sharingControls(gtx C) D {
	a.mu.Lock()
	stale := !a.sharing.loading && time.Since(a.sharing.refreshed) > sharingRefresh
	if stale {
		a.sharing.loading = true
	}
	rows := a.sharing.rows
	busy := a.sharing.busy
	msg := a.sharing.msg
	a.mu.Unlock()
	if stale {
		go a.refreshShared()
	}

	for len(a.sharing.stop) < len(rows) {
		a.sharing.stop = append(a.sharing.stop, widget.Clickable{})
	}
	for i := range rows {
		if a.sharing.stop[i].Clicked(gtx) && !busy {
			a.stopSharing(rows[i].TagsetID)
		}
	}

	widgets := []layout.Widget{
		func(gtx C) D { return a.sectionTitle(gtx, "Sharing") },
		func(gtx C) D {
			return a.sectionHint(gtx,
				"What this device publishes to the madnetwork — nothing, until you choose. "+
					"Share an album from its own page in the library; what you share is visible "+
					"to your paired nodes (or the whole community), attributable to this node, "+
					"and redistributed by others. This list is everything currently shared.")
		},
	}
	if msg != "" {
		widgets = append(widgets, func(gtx C) D {
			return layout.Inset{Bottom: 8}.Layout(gtx, func(gtx C) D {
				l := material.Caption(a.th, msg)
				l.Color = colDim
				return l.Layout(gtx)
			})
		})
	}
	if len(rows) == 0 {
		widgets = append(widgets, func(gtx C) D {
			return a.sectionHint(gtx, "Nothing is shared right now. Downloaded music is still "+
				"seeded back to the network — that is the swarm working, not your library opening.")
		})
	}
	for i := range rows {
		widgets = append(widgets, a.sharedRow(i, rows[i], busy))
	}
	return pairingList(gtx, widgets)
}

// sharedRow is one published track: what it is, who can see it, and Stop.
func (a *App) sharedRow(i int, r backend.SharedTrack, busy bool) layout.Widget {
	return func(gtx C) D {
		return layout.Inset{Top: 8}.Layout(gtx, func(gtx C) D {
			return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
				layout.Flexed(1, func(gtx C) D {
					return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
						layout.Rigid(func(gtx C) D {
							l := material.Body2(a.th, r.Title)
							l.Color = colFg
							l.MaxLines = 1
							return l.Layout(gtx)
						}),
						layout.Rigid(func(gtx C) D {
							scope := "paired nodes"
							if r.Scope == backend.ShareMadnetwork {
								scope = "whole madnetwork"
							}
							l := material.Caption(a.th, r.Artist+" · "+r.Album+" · "+scope)
							l.Color = colDim
							l.MaxLines = 1
							return l.Layout(gtx)
						}),
					)
				}),
				layout.Rigid(func(gtx C) D {
					return a.smallButton(gtx, &a.sharing.stop[i], "Stop", busy)
				}),
			)
		})
	}
}

func (a *App) refreshShared() {
	rows, err := a.be.Published(context.Background())
	a.mu.Lock()
	a.sharing.loading = false
	a.sharing.refreshed = time.Now()
	if err != nil {
		a.sharing.msg = err.Error()
	} else {
		a.sharing.rows = rows
		a.sharing.msg = ""
	}
	a.mu.Unlock()
	a.win.Invalidate()
}

func (a *App) stopSharing(tagsetID int64) {
	a.mu.Lock()
	if a.sharing.busy {
		a.mu.Unlock()
		return
	}
	a.sharing.busy = true
	a.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		err := a.be.SetShareScope(ctx, []int64{tagsetID}, backend.ShareNotShared)
		a.mu.Lock()
		a.sharing.busy = false
		if err != nil {
			a.sharing.msg = err.Error()
		}
		// Whatever happened, re-read: the list must show the store, not the hope.
		a.sharing.refreshed = time.Time{}
		// The header control may be showing this album; make it re-ask too.
		a.sharing.albumKey, a.sharing.scopes = "", nil
		a.mu.Unlock()
		a.win.Invalidate()
	}()
}
