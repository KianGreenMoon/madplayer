package ui

// The first-run connect step (madshare docs/plans/full-node-mode.md P3): the
// guided way from "node mode is on" to "a reachable member".
//
// The three ways onto the underlay all existed before this — a server's
// peering dialled by the enrolment loop, multicast on the local network, and
// a typed peer (peers.go) — and so did pairing, the key backup and the tray.
// What a person switching node mode on did NOT have was the order, or any
// way to see which of those had happened. So this is a checklist, not a
// wizard: five steps in the order they depend on each other, each with what
// is true right now and the act that completes it, shown on the Node mode
// page exactly while the mode is on. A Gio program has no modal to put a
// wizard in and no reason to want one; the page IS the first run, and it
// stays useful afterwards as the place that says which link is down.
//
// Open question 6 of the plan (backbone thinness) is answered in the words
// of the peer step: a friend's server is the address to paste first.

import (
	"fmt"
	"image"
	"os"
	"time"

	"gioui.org/layout"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"
)

// connectStep is one row: what it is, whether it is done, what is true, and
// the act that gets it done when there is one to offer.
type connectStep struct {
	title string
	done  bool
	state string
	// act opens the page where the step is completed; actLabel names it. Nil
	// when the act is on this page already or there is nothing to do yet.
	act      func()
	actLabel string
	// peerBox puts the underlay peer box under the row — the one step whose
	// act is small enough to do in place.
	peerBox bool
}

// connectState is what the settings index says after "On": the first step
// still to do, or that every one is done.
func (a *App) connectState() string {
	for _, s := range a.connectSteps() {
		if !s.done {
			return "next: " + lower(s.title)
		}
	}
	return "all set · " + a.pairingSummary()
}

func lower(s string) string {
	if s == "" {
		return s
	}
	b := []byte(s)
	if b[0] >= 'A' && b[0] <= 'Z' {
		b[0] += 'a' - 'A'
	}
	return string(b)
}

// connectSteps reads what is already in memory — the index is laid out sixty
// times a second — and computes the five rows.
func (a *App) connectSteps() []connectStep {
	_, meshUp := a.be.Mesh()
	problem := a.be.MeshProblem()

	a.mu.Lock()
	meshWanted := a.cfg.Mesh
	typed := len(a.cfg.MeshPeers)
	servers := len(a.cfg.Servers)
	keyBackup := a.cfg.KeyBackup
	tray := a.cfg.Tray
	live := a.underlay
	peers := a.pairing.peers
	refreshed := !a.pairing.refreshed.IsZero()
	a.mu.Unlock()

	// 1. The mesh.
	mesh := connectStep{title: "The madnetwork is on"}
	switch {
	case problem != "":
		mesh.state = problem
	case meshUp:
		mesh.done, mesh.state = true, "On."
	case meshWanted:
		mesh.state = "Switched on — it comes up when madplayer restarts."
	default:
		mesh.state = "Off. A node needs the mesh."
		mesh.act, mesh.actLabel = func() { a.openSettingsPage(pageNetwork) }, "Open The madnetwork"
	}

	// 2. The underlay.
	reach := connectStep{title: "Reaching the mesh"}
	up, inbound, down := 0, 0, ""
	for _, p := range live {
		if p.Up {
			up++
			if p.Inbound {
				inbound++
			}
		} else if down == "" && p.Problem != "" {
			down = p.Problem
		}
	}
	offered := 0
	var roundProblem string
	if a.enrol != nil {
		for _, r := range a.enrol.Status() {
			offered += r.Peers
			if roundProblem == "" {
				roundProblem = r.Problem
			}
		}
	}
	switch {
	case !meshUp:
		reach.state = "After the madnetwork is on."
	case up > 0:
		reach.done = true
		reach.state = plural(up, "connection") + " up"
		if inbound > 0 {
			reach.state += fmt.Sprintf(", %d dialled in", inbound)
		}
		reach.state += "."
	case offered > 0:
		reach.state = fmt.Sprintf("Your server offered %d address(es); none has connected yet.", offered)
		if down != "" {
			reach.state += " Last problem: " + down
		}
		reach.peerBox = true
	case typed > 0:
		reach.state = "The typed address has not connected yet."
		if down != "" {
			reach.state += " " + down
		}
		reach.peerBox = true
	default:
		reach.state = "Nobody yet. On the same network as a madshare it is found by itself " +
			"within a minute. Otherwise paste an address: a friend's server (its listen " +
			"address) is the best one — a public yggdrasil peer works too, but then the " +
			"community's traffic goes through strangers' relays."
		if servers == 0 && roundProblem == "" {
			reach.state += " Signing in to a server under “The madnetwork” gets its addresses automatically."
		}
		if roundProblem != "" {
			reach.state += " (" + roundProblem + ")"
		}
		reach.peerBox = true
	}

	// 3. Pairing.
	pair := connectStep{title: "Paired with a node"}
	friends, waiting := 0, 0
	for _, p := range peers {
		switch p.State {
		case "friend":
			friends++
		case "pending_incoming", "pending_outgoing":
			waiting++
		}
	}
	openPairing := func() { a.openSettingsPage(pagePairing) }
	switch {
	case !meshUp:
		pair.state = "After the madnetwork is on."
	case friends > 0:
		pair.done = true
		pair.state = plural(friends, "paired node")
		if waiting > 0 {
			pair.state += fmt.Sprintf(", %d waiting", waiting)
		}
		pair.state += "."
	case waiting > 0:
		pair.state = fmt.Sprintf("Waiting for %d to accept. Friendship needs both sides.", waiting)
		pair.act, pair.actLabel = openPairing, "Open Paired nodes"
	case !refreshed:
		pair.state = "Reading the peer table…"
	default:
		pair.state = "Copy this node's card into a server's Network page, or paste that server's card."
		pair.act, pair.actLabel = openPairing, "Open Paired nodes"
	}

	// 4. The key.
	key := connectStep{title: "Key backed up"}
	switch {
	case keyBackup == "":
		key.state = "Not yet. The key is this node's identity: without a copy, a lost disk means " +
			"pairing everything again."
		if meshUp {
			key.act, key.actLabel = openPairing, "Open Paired nodes"
		}
	case !exists(keyBackup):
		key.state = "Was backed up to " + keyBackup + ", which is not there any more."
		key.act, key.actLabel = openPairing, "Open Paired nodes"
	default:
		key.done, key.state = true, "Backed up to "+keyBackup+"."
	}

	// 5. Presence.
	presence := connectStep{title: "Staying reachable"}
	autostartOn, _ := a.autostartState()
	switch {
	case tray && autostartOn:
		presence.done, presence.state = true, "In the tray when the window closes, started at login."
	case tray:
		presence.state = "In the tray when the window closes. Below: start at login too."
	default:
		presence.state = "Below: keep running in the tray, and start at login — a node that is only " +
			"up while its window is open is one the network keeps writing off."
	}

	return []connectStep{mesh, reach, pair, key, presence}
}

// autostartState is the login entry's cached truth (tray.go re-reads it on
// the page's cadence), safe from any goroutine.
func (a *App) autostartState() (bool, string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.autostartOn.Value, a.autostartExe
}

// connectControls is the checklist.
func (a *App) connectControls(gtx C) D {
	a.wantUnderlay()
	steps := a.connectSteps()
	for len(a.connect.act) < len(steps) {
		a.connect.act = append(a.connect.act, widget.Clickable{})
	}
	for i := range steps {
		if steps[i].act != nil && a.connect.act[i].Clicked(gtx) {
			steps[i].act()
		}
	}
	if a.btnAddPeer.Clicked(gtx) {
		a.addPeer(a.peerEd.Text())
	}
	a.mu.Lock()
	peerMsg := a.peerMsg
	a.mu.Unlock()

	rows := []layout.Widget{
		func(gtx C) D {
			return layout.Inset{Top: 12}.Layout(gtx, func(gtx C) D {
				l := material.Body2(a.th, "Becoming a reachable member")
				l.Color = colFg
				return l.Layout(gtx)
			})
		},
	}
	for i, s := range steps {
		rows = append(rows, a.connectRow(i, s))
		if s.peerBox {
			rows = append(rows, func(gtx C) D {
				return layout.Inset{Top: 6, Left: unit.Dp(28)}.Layout(gtx, func(gtx C) D {
					return a.clipRow(gtx, &a.clipPeer, &a.peerEd, "tls://example.org:7743", true,
						layout.Rigid(func(gtx C) D {
							return a.actionButton(gtx, &a.btnAddPeer, iconAddPeer, false)
						}),
					)
				})
			})
			if peerMsg != "" {
				rows = append(rows, func(gtx C) D {
					return layout.Inset{Top: 4, Left: unit.Dp(28)}.Layout(gtx, func(gtx C) D {
						l := material.Caption(a.th, peerMsg)
						l.Color = colDim
						return l.Layout(gtx)
					})
				})
			}
		}
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, rigidAll(rows)...)
}

// connectRow is one step: the mark, the title, the line, and the act.
func (a *App) connectRow(i int, s connectStep) layout.Widget {
	return func(gtx C) D {
		return layout.Inset{Top: 10}.Layout(gtx, func(gtx C) D {
			return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Start}.Layout(gtx,
				layout.Rigid(func(gtx C) D {
					ic, col := iconTodo, colDim
					if s.done {
						ic, col = iconKept, colAccent
					}
					// The inset first, the exact size inside it: an inset takes
					// its space out of the constraints, and the icon draws at
					// the minimum it is given.
					return layout.Inset{Right: 10, Top: 1}.Layout(gtx, func(gtx C) D {
						px := gtx.Dp(18)
						gtx.Constraints = layout.Exact(image.Pt(px, px))
						return ic.Layout(gtx, col)
					})
				}),
				layout.Flexed(1, func(gtx C) D {
					return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
						layout.Rigid(func(gtx C) D {
							l := material.Body2(a.th, s.title)
							l.Color = colFg
							return l.Layout(gtx)
						}),
						layout.Rigid(func(gtx C) D {
							l := material.Caption(a.th, s.state)
							l.Color = colDim
							return l.Layout(gtx)
						}),
					)
				}),
				layout.Rigid(func(gtx C) D {
					if s.act == nil {
						return D{}
					}
					return layout.Inset{Left: 8}.Layout(gtx, func(gtx C) D {
						return a.smallButton(gtx, &a.connect.act[i], s.actLabel, false)
					})
				}),
			)
		})
	}
}

// wantUnderlay keeps the underlay snapshot fresh while a page reads it, on
// the peers page's cadence and never in a layout function: the read blocks
// on the yggdrasil core's link actor. Shared by the peers page and the
// checklist.
func (a *App) wantUnderlay() {
	if _, meshUp := a.be.Mesh(); !meshUp {
		return
	}
	a.mu.Lock()
	stale := !a.underlayLoading && time.Since(a.underlayAt) > peerRefresh
	if stale {
		a.underlayLoading = true
	}
	a.mu.Unlock()
	if stale {
		go a.refreshUnderlay()
	}
}

// connectState holds the checklist's clickables.
type connectWidgets struct {
	act []widget.Clickable
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
