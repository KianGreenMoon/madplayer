package ui

// Node mode: this device as a REGULAR member of the madnetwork instead of a
// quiet listener. The design is madshare's docs/plans/full-node-mode.md; the
// client-side reading is docs/design.md §"Node mode". What the switch does is
// deliberately small: it reveals the pages that administrate the node — the
// paired-nodes page today, publishing and seeding controls as they are built —
// and hides them again. Membership itself lives in the backend's peer table,
// so switching the mode off does not unfriend anyone, and the caption below
// the switch says so when that state exists rather than letting the menus
// vanish over a membership that is still standing.
//
// This graduates the 2026-08-17 pairing experiment (full-node-mode.md P1):
// the const that switched it is gone and prefs.NodeMode is the switch now.
// madshare's app.Pairing underneath is still marked EXPERIMENTAL — settling
// its method set (block? rename?) is that repo's W1, a madshare question.

import (
	"gioui.org/layout"
	"gioui.org/widget/material"
)

// nodeMode reports whether node mode is on in this build. It is the one
// question every gated page asks, so the desktop-only rule (full-node-mode.md:
// phones stay listeners) lives here and cannot be skipped by one of them.
func (a *App) nodeMode() bool {
	if !nodeModeOffered {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cfg.NodeMode
}

// nodeModeState is the index row's one line: which kind of node this device is
// right now. When the mode is on it borrows the paired-nodes summary, so the
// index answers the likely question ("am I paired yet?") without a tap.
func (a *App) nodeModeState() string {
	if !a.nodeMode() {
		return "Off — this device is a quiet listener"
	}
	if s := a.pairingSummary(); s != "" {
		return "On · " + s
	}
	return "On"
}

// nodeModeControls is the page: what the mode is, the switch, and one line of
// what is true right now — the same shape as the madnetwork page's switch.
func (a *App) nodeModeControls(gtx C) D {
	if a.nodeModeOn.Update(gtx) {
		a.saveNodeMode(a.nodeModeOn.Value)
	}

	_, meshUp := a.be.Mesh()
	a.mu.Lock()
	on := a.cfg.NodeMode
	msg := a.nodeModeMsg
	paired := 0
	if !a.pairing.refreshed.IsZero() {
		for _, p := range a.pairing.peers {
			if p.State == "friend" {
				paired++
			}
		}
	}
	a.mu.Unlock()

	return layout.Inset{Top: 16}.Layout(gtx, func(gtx C) D {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx C) D { return a.sectionTitle(gtx, "Node mode") }),
			layout.Rigid(func(gtx C) D {
				return a.sectionHint(gtx,
					"Run this device as a regular node of the madnetwork: it can pair with "+
						"servers and other devices by exchanged keys, and it appears on the "+
						"community's maps like any server. Off, it stays a quiet listener — "+
						"signed in to its servers, invisible to everyone else. Either way "+
						"your own music folders are never shared; sharing stays a separate, "+
						"per-item choice.")
			}),
			layout.Rigid(func(gtx C) D {
				cb := material.CheckBox(a.th, &a.nodeModeOn, "Act as a regular node")
				cb.Color, cb.IconColor = colFg, colFg
				return cb.Layout(gtx)
			}),
			layout.Rigid(func(gtx C) D {
				// One line for what is actually true. The cases are different
				// problems: a mode that is on over a mesh that is off, a mode
				// switched off over friendships that still stand, and the two
				// plain states.
				var txt string
				switch {
				case msg != "":
					txt = msg
				case on && !meshUp:
					txt = "On, but the madnetwork is off — a node needs the mesh. " +
						"Switch it on under “The madnetwork”."
				case on:
					txt = "On. Manage friendships under “Paired nodes”."
				case paired > 0:
					txt = plural(paired, "paired node") + " remains — this device stays a " +
						"member until they are removed. Switch node mode on to manage them."
				default:
					txt = "Off. This device is a listener: it uses the madnetwork through " +
						"the servers it is signed in to."
				}
				return layout.Inset{Top: 8}.Layout(gtx, func(gtx C) D {
					l := material.Caption(a.th, txt)
					l.Color = colDim
					return l.Layout(gtx)
				})
			}),
		)
	})
}

// saveNodeMode records the switch. Unlike the mesh switch there is nothing to
// restart: the pages it gates are laid out from the pref on the next frame.
func (a *App) saveNodeMode(on bool) {
	a.mu.Lock()
	a.cfg.NodeMode = on
	cfg := a.cfg
	a.nodeModeMsg = ""
	a.mu.Unlock()
	if err := a.store.Save(cfg); err != nil {
		a.mu.Lock()
		a.nodeModeMsg = "could not save the node mode setting: " + err.Error()
		a.mu.Unlock()
	}
}
