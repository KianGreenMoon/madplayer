package ui

// The pairing test: befriending a server by exchanged public keys, the way two
// madshare servers do on /admin/network — own card out, their card (or bare
// key) in, and the peer table with its states.
//
// This is an EXPERIMENT, switched by pairingEnabled below. It deliberately
// crosses the household design (a device that appears in nobody's friend
// list): a paired device is an ordinary community member with a gossiped edge
// and a place on every map, which is exactly the trade the owner wants to see
// on a real device before judging it (2026-08-17). Flip the const to false to
// take the section out of Settings; removing the experiment entirely is
// deleting this file, backend/pairing.go, the pairEd field (app.go, keys.go)
// and the one row in panels.go.
//
// Even paired, this node publishes nothing — the library is pinned closed and
// only the seeded cache is served — so the test costs presence, not privacy.

import (
	"context"
	"io"
	"strings"
	"time"

	"gioui.org/io/clipboard"
	"gioui.org/layout"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"daemonlord.ygg/madplayer/internal/backend"
)

// pairingEnabled is the light switch for the whole experiment. False removes
// the Settings section; nothing else in the program calls any of this.
const pairingEnabled = true

// pairingRefresh is how stale the peer table may be while the section is on
// screen. The interesting moment — "waiting for their accept" flipping to
// "friends" — happens on the other admin's clock, so the table re-reads itself
// at this cadence rather than only on a click. It is a query against a
// friend-list-sized table, and it runs only while Settings is open.
const pairingRefresh = 5 * time.Second

// pairingState is everything the section holds besides the editor (which must
// be a top-level App field so the typing-gate reflection test can see it).
// The widget fields belong to the UI goroutine; the rest is guarded by App.mu.
type pairingState struct {
	btnAdd, btnCopy widget.Clickable
	accept, remove  []widget.Clickable

	// under App.mu
	peers     []backend.Peer
	ident     backend.NodeIdentity
	identOK   bool
	refreshed time.Time
	loading   bool
	busy      bool
	msg       string
	clearEd   bool
}

// pairingControls is the Settings section.
func (a *App) pairingControls(gtx C) D {
	if !pairingEnabled {
		return D{}
	}
	_, meshUp := a.be.Mesh()

	a.mu.Lock()
	stale := meshUp && !a.pairing.loading && time.Since(a.pairing.refreshed) > pairingRefresh
	if stale {
		a.pairing.loading = true
	}
	peers := a.pairing.peers
	ident, identOK := a.pairing.ident, a.pairing.identOK
	busy, msg := a.pairing.busy, a.pairing.msg
	if a.pairing.clearEd {
		a.pairing.clearEd = false
		defer a.pairEd.SetText("")
	}
	a.mu.Unlock()
	if stale {
		go a.refreshPairing()
	}

	if a.pairing.btnCopy.Clicked(gtx) && identOK {
		gtx.Execute(clipboard.WriteCmd{
			Type: "application/text",
			Data: io.NopCloser(strings.NewReader(ident.Card)),
		})
		a.setPairMsg("Node card copied — hand it to the server's admin")
	}
	if a.pairing.btnAdd.Clicked(gtx) && !busy {
		a.pairWith(a.pairEd.Text())
	}
	for len(a.pairing.accept) < len(peers) {
		a.pairing.accept = append(a.pairing.accept, widget.Clickable{})
		a.pairing.remove = append(a.pairing.remove, widget.Clickable{})
	}
	for i := range peers {
		if a.pairing.accept[i].Clicked(gtx) && !busy {
			a.acceptPair(peers[i].ID)
		}
		if a.pairing.remove[i].Clicked(gtx) && !busy {
			a.removePair(peers[i].ID)
		}
	}

	rows := []layout.Widget{
		func(gtx C) D { return a.sectionTitle(gtx, "Node pairing (test)") },
		func(gtx C) D {
			return a.sectionHint(gtx,
				"Connect this device to a server the way servers connect to each other: by "+
					"exchanged keys, no account. Copy this node's card into the server's "+
					"Network page, or paste that server's card (or bare key) here — friendship "+
					"needs both sides. An experiment: a paired device is a visible member of "+
					"the madnetwork, not a quiet listener.")
		},
	}

	if !meshUp {
		rows = append(rows, func(gtx C) D {
			return a.sectionHint(gtx, "The madnetwork is off — pairing needs the mesh (see above).")
		})
		return pairingList(gtx, rows)
	}

	if identOK {
		rows = append(rows, func(gtx C) D {
			return layout.Inset{Top: 6}.Layout(gtx, func(gtx C) D {
				return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
					layout.Flexed(1, func(gtx C) D {
						return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
							layout.Rigid(func(gtx C) D {
								l := material.Body2(a.th, "This node: "+ident.Name)
								l.Color = colFg
								l.MaxLines = 1
								return l.Layout(gtx)
							}),
							layout.Rigid(func(gtx C) D {
								l := material.Caption(a.th, shortKey(ident.Key)+" · "+ident.Address)
								l.Color = colDim
								l.MaxLines = 1
								return l.Layout(gtx)
							}),
						)
					}),
					layout.Rigid(func(gtx C) D {
						return a.smallButton(gtx, &a.pairing.btnCopy, "Copy node card", false)
					}),
				)
			})
		})
	}

	rows = append(rows, func(gtx C) D {
		return layout.Inset{Top: 8}.Layout(gtx, func(gtx C) D {
			return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
				layout.Flexed(1, func(gtx C) D {
					ed := material.Editor(a.th, &a.pairEd, `their card {"madshare_node_card":…} or public key`)
					ed.Color, ed.HintColor = colFg, colDim
					return filled(gtx, colSel, ed.Layout)
				}),
				layout.Rigid(layout.Spacer{Width: 8}.Layout),
				layout.Rigid(func(gtx C) D {
					label := "Pair"
					if busy {
						label = "Pairing…"
					}
					return a.smallButton(gtx, &a.pairing.btnAdd, label, busy)
				}),
			)
		})
	})

	if msg != "" {
		rows = append(rows, func(gtx C) D {
			return layout.Inset{Top: 8}.Layout(gtx, func(gtx C) D {
				l := material.Caption(a.th, msg)
				l.Color = colDim
				return l.Layout(gtx)
			})
		})
	}

	for i := range peers {
		rows = append(rows, a.peerRow(i, peers[i], busy))
	}

	return pairingList(gtx, rows)
}

func pairingList(gtx C, rows []layout.Widget) D {
	return layout.Inset{Top: 16}.Layout(gtx, func(gtx C) D {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, rigidAll(rows)...)
	})
}

func rigidAll(rows []layout.Widget) []layout.FlexChild {
	out := make([]layout.FlexChild, len(rows))
	for i, r := range rows {
		out[i] = layout.Rigid(r)
	}
	return out
}

// peerRow is one known node: who it is, where the friendship stands, and the
// action that stands open.
func (a *App) peerRow(i int, p backend.Peer, busy bool) layout.Widget {
	return func(gtx C) D {
		return layout.Inset{Top: 8}.Layout(gtx, func(gtx C) D {
			return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
				layout.Flexed(1, func(gtx C) D {
					return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
						layout.Rigid(func(gtx C) D {
							name := p.Name
							if name == "" {
								name = shortKey(p.Key)
							}
							l := material.Body2(a.th, name)
							l.Color = colFg
							l.MaxLines = 1
							return l.Layout(gtx)
						}),
						layout.Rigid(func(gtx C) D {
							l := material.Caption(a.th, peerStateText(p)+" · "+shortKey(p.Key))
							l.Color = colDim
							l.MaxLines = 1
							return l.Layout(gtx)
						}),
					)
				}),
				layout.Rigid(func(gtx C) D {
					if p.State != "pending_incoming" {
						return D{}
					}
					return layout.Inset{Right: 8}.Layout(gtx, func(gtx C) D {
						return a.smallButton(gtx, &a.pairing.accept[i], "Accept", busy)
					})
				}),
				layout.Rigid(func(gtx C) D {
					return a.smallButton(gtx, &a.pairing.remove[i], "Remove", busy)
				}),
			)
		})
	}
}

// peerStateText is a trust state in the words of the person waiting on it.
func peerStateText(p backend.Peer) string {
	switch p.State {
	case "friend":
		if p.LastSeen.IsZero() {
			return "Friends"
		}
		return "Friends · seen " + p.LastSeen.Format("15:04")
	case "pending_outgoing":
		return "Asked — waiting for their accept"
	case "pending_incoming":
		return "Asks to be friends"
	case "blocked":
		return "Blocked"
	default:
		return p.State
	}
}

// shortKey is the readable form of a 64-hex key: enough to compare against the
// other end's screen, short enough for one line.
func shortKey(k string) string {
	if len(k) <= 16 {
		return k
	}
	return k[:8] + "…" + k[len(k)-8:]
}

// refreshPairing re-reads the identity and the peer table off the UI
// goroutine. The identity is re-read with the peers because both come from the
// node, and the node arrives after the window does.
func (a *App) refreshPairing() {
	ident, identOK := a.be.NodeIdentity()
	peers, err := a.be.Peers(context.Background())
	a.mu.Lock()
	a.pairing.loading = false
	a.pairing.refreshed = time.Now()
	a.pairing.ident, a.pairing.identOK = ident, identOK
	if err == nil {
		a.pairing.peers = peers
	} else if a.pairing.msg == "" {
		a.pairing.msg = err.Error()
	}
	a.mu.Unlock()
	a.win.Invalidate()
}

// pairAction runs one pairing act at a time, reports on the section's own
// line, and re-reads the table so the row and the message agree.
func (a *App) pairAction(run func(ctx context.Context) (string, error)) {
	a.mu.Lock()
	if a.pairing.busy {
		a.mu.Unlock()
		return
	}
	a.pairing.busy = true
	a.mu.Unlock()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		msg, err := run(ctx)
		a.mu.Lock()
		a.pairing.busy = false
		if err != nil {
			a.pairing.msg = err.Error()
		} else {
			a.pairing.msg = msg
			a.pairing.clearEd = true
		}
		a.mu.Unlock()
		a.refreshPairing()
	}()
}

func (a *App) pairWith(input string) {
	a.pairAction(func(ctx context.Context) (string, error) {
		p, err := a.be.PairWith(ctx, input)
		if err != nil {
			return "", err
		}
		if p.State == "friend" {
			return "Friends — they had already asked", nil
		}
		return "Asked. Friendship completes when their admin accepts this node", nil
	})
}

func (a *App) acceptPair(id int64) {
	a.pairAction(func(ctx context.Context) (string, error) {
		if err := a.be.AcceptPeer(ctx, id); err != nil {
			return "", err
		}
		return "Accepted", nil
	})
}

func (a *App) removePair(id int64) {
	a.pairAction(func(ctx context.Context) (string, error) {
		if err := a.be.RemovePeer(ctx, id); err != nil {
			return "", err
		}
		return "Removed", nil
	})
}

// setPairMsg writes the section's status line from the UI goroutine.
func (a *App) setPairMsg(s string) {
	a.mu.Lock()
	a.pairing.msg = s
	a.mu.Unlock()
}
