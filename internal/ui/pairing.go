package ui

// The paired-nodes page: befriending a server (or another device) by exchanged
// public keys, the way two madshare servers do on /admin/network — own card
// out, their card (or bare key) in, and the peer table with its states.
//
// This is node mode's first administration page (nodemode.go; the design is
// madshare's docs/plans/full-node-mode.md). It began as the 2026-08-17 pairing
// experiment behind a const; prefs.NodeMode is the switch now, read through
// settingsSections' hidden rule, so the page exists exactly while the mode is
// on. A device paired here is a full community member — a gossiped edge, a
// place on every map, exactly like a server; the quiet listener path stays
// what an unpaired device gets.
//
// Sharing is separate and unchanged: the library stays pinned closed and only
// the seeded cache is served, paired or not.

import (
	"context"
	"errors"
	"io"
	"strings"
	"time"

	"gioui.org/io/clipboard"
	"gioui.org/layout"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"daemonlord.ygg/madplayer/internal/backend"
)

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
	btnAdd, btnCopy       widget.Clickable
	btnBackUp, btnRestore widget.Clickable
	accept, remove        []widget.Clickable
	rename, block         []widget.Clickable
	unblock               []widget.Clickable
	// The inline editor under one row (peerEdit.go): which peer, for which
	// act, and its two buttons. UI goroutine only.
	editing       int64
	editKind      string // "rename" or "block"
	btnEditOK     widget.Clickable
	btnEditCancel widget.Clickable

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

// wantPeerTable keeps the peer table fresh while some page is reading it: a
// bounded re-read on the pairing cadence. Shared by the paired-nodes page and
// the node-mode page — whose caption counts friendships even while the pairing
// page is hidden (mode off, memberships kept), so the count cannot depend on
// that page's own layout ever running. A no-op while the mesh is down: there
// is no node to ask.
func (a *App) wantPeerTable() {
	if _, meshUp := a.be.Mesh(); !meshUp {
		return
	}
	a.mu.Lock()
	stale := !a.pairing.loading && time.Since(a.pairing.refreshed) > pairingRefresh
	if stale {
		a.pairing.loading = true
	}
	a.mu.Unlock()
	if stale {
		go a.refreshPairing()
	}
}

// pairingControls is the page.
func (a *App) pairingControls(gtx C) D {
	_, meshUp := a.be.Mesh()
	a.wantPeerTable()

	a.mu.Lock()
	peers := a.pairing.peers
	ident, identOK := a.pairing.ident, a.pairing.identOK
	busy, msg := a.pairing.busy, a.pairing.msg
	if a.pairing.clearEd {
		a.pairing.clearEd = false
		defer a.pairEd.SetText("")
	}
	a.mu.Unlock()

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
	if a.pairing.btnBackUp.Clicked(gtx) && !busy {
		a.backUpKey(a.keyPathEd.Text())
	}
	if a.pairing.btnRestore.Clicked(gtx) && !busy {
		a.restoreKey(a.keyPathEd.Text())
	}
	for len(a.pairing.accept) < len(peers) {
		a.pairing.accept = append(a.pairing.accept, widget.Clickable{})
		a.pairing.remove = append(a.pairing.remove, widget.Clickable{})
		a.pairing.rename = append(a.pairing.rename, widget.Clickable{})
		a.pairing.block = append(a.pairing.block, widget.Clickable{})
		a.pairing.unblock = append(a.pairing.unblock, widget.Clickable{})
	}
	for i := range peers {
		if a.pairing.accept[i].Clicked(gtx) && !busy {
			a.acceptPair(peers[i].ID)
		}
		if a.pairing.remove[i].Clicked(gtx) && !busy {
			a.removePair(peers[i].ID)
		}
		if a.pairing.rename[i].Clicked(gtx) && !busy {
			a.beginPeerEdit(peers[i], "rename")
		}
		if a.pairing.block[i].Clicked(gtx) && !busy {
			a.beginPeerEdit(peers[i], "block")
		}
		if a.pairing.unblock[i].Clicked(gtx) && !busy {
			a.unblockPair(peers[i].ID)
		}
	}
	if a.pairing.btnEditOK.Clicked(gtx) && !busy {
		a.commitPeerEdit()
	}
	if a.pairing.btnEditCancel.Clicked(gtx) {
		a.cancelPeerEdit()
	}

	rows := []layout.Widget{
		func(gtx C) D { return a.sectionTitle(gtx, "Paired nodes") },
		func(gtx C) D {
			return a.sectionHint(gtx,
				"Connect this device to a server the way servers connect to each other: by "+
					"exchanged keys, no account. Copy this node's card into the server's "+
					"Network page, or paste that server's card (or bare key) here — friendship "+
					"needs both sides. A paired device is a visible member of the madnetwork, "+
					"on the community's maps, not a quiet listener.")
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

	rows = append(rows, a.nodeKeyRows(busy)...)

	rows = append(rows, func(gtx C) D {
		return layout.Inset{Top: 8}.Layout(gtx, func(gtx C) D {
			// The paste button beside this box is the whole reason pairing can be
			// done from a phone at all: the card is a few hundred characters of
			// JSON that arrives in a message, and Gio offers no other way in.
			return a.clipRow(gtx, &a.clipCard, &a.pairEd,
				`their card {"madshare_node_card":…} or public key`, true,
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
		if a.pairing.editing == peers[i].ID && a.pairing.editKind != "" {
			rows = append(rows, a.peerEditRow(peers[i], busy))
		}
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
					return layout.Inset{Right: 8}.Layout(gtx, func(gtx C) D {
						return a.smallButton(gtx, &a.pairing.rename[i], "Rename", busy)
					})
				}),
				layout.Rigid(func(gtx C) D {
					// Block and Unblock are the same slot: a row is one or the
					// other. Self-defence is not optional for a personal node,
					// and a block is public — the reason travels with it.
					return layout.Inset{Right: 8}.Layout(gtx, func(gtx C) D {
						if p.State == "blocked" {
							return a.smallButton(gtx, &a.pairing.unblock[i], "Unblock", busy)
						}
						return a.smallButton(gtx, &a.pairing.block[i], "Block", busy)
					})
				}),
				layout.Rigid(func(gtx C) D {
					return a.smallButton(gtx, &a.pairing.remove[i], "Remove", busy)
				}),
			)
		})
	}
}

// ── Rename and block: an editor under the row ────────────────────────────────
//
// Both acts take a line of text — a name, or the reason a block carries onto
// the network — so pressing either opens one editor under that row rather
// than a dialog Gio does not have: the box, the act's own button, Cancel.
// One editor serves both, hinted for the act; a second press elsewhere moves
// it.

// beginPeerEdit opens the editor under a row.
func (a *App) beginPeerEdit(p backend.Peer, kind string) {
	a.pairing.editing, a.pairing.editKind = p.ID, kind
	if kind == "rename" {
		a.peerEditEd.SetText(p.Name)
	} else {
		a.peerEditEd.SetText("")
	}
}

func (a *App) cancelPeerEdit() {
	a.pairing.editing, a.pairing.editKind = 0, ""
	a.peerEditEd.SetText("")
}

// commitPeerEdit runs the act the open editor is for.
func (a *App) commitPeerEdit() {
	id, kind, text := a.pairing.editing, a.pairing.editKind, a.peerEditEd.Text()
	if id == 0 || kind == "" {
		return
	}
	a.cancelPeerEdit()
	switch kind {
	case "rename":
		a.renamePair(id, text)
	case "block":
		a.blockPair(id, text)
	}
}

// peerEditRow is the editor: box, act, cancel.
func (a *App) peerEditRow(p backend.Peer, busy bool) layout.Widget {
	return func(gtx C) D {
		hint, label := "What you call this node", "Save"
		if a.pairing.editKind == "block" {
			hint, label = "Why — shown to the whole network with the block", "Block"
		}
		return layout.Inset{Top: 6, Left: 12}.Layout(gtx, func(gtx C) D {
			return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
				layout.Flexed(1, func(gtx C) D {
					e := material.Editor(a.th, &a.peerEditEd, hint)
					e.Color, e.HintColor = colFg, colDim
					return e.Layout(gtx)
				}),
				layout.Rigid(func(gtx C) D {
					return layout.Inset{Left: 8}.Layout(gtx, func(gtx C) D {
						return a.smallButton(gtx, &a.pairing.btnEditOK, label, busy)
					})
				}),
				layout.Rigid(func(gtx C) D {
					return layout.Inset{Left: 8}.Layout(gtx, func(gtx C) D {
						return a.smallButton(gtx, &a.pairing.btnEditCancel, "Cancel", false)
					})
				}),
			)
		})
	}
}

func (a *App) renamePair(id int64, name string) {
	a.pairAction(func(ctx context.Context) (string, error) {
		if err := a.be.RenamePeer(ctx, id, name); err != nil {
			return "", err
		}
		if strings.TrimSpace(name) == "" {
			return "Name cleared — the node's own name shows again", nil
		}
		return "Renamed", nil
	})
}

func (a *App) blockPair(id int64, reason string) {
	a.pairAction(func(ctx context.Context) (string, error) {
		if err := a.be.BlockPeer(ctx, id, reason); err != nil {
			return "", err
		}
		return "Blocked — the network sees the block and its reason; the nodes it introduced " +
			"are out of your community with it", nil
	})
}

func (a *App) unblockPair(id int64) {
	a.pairAction(func(ctx context.Context) (string, error) {
		if err := a.be.UnblockPeer(ctx, id); err != nil {
			return "", err
		}
		return "Unblocked — the row is back where it was", nil
	})
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
	// Bounded for the same reason as refreshShared: an unanswered read must
	// not pin loading=true forever and end the refresh cycle with it.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	ident, identOK := a.be.NodeIdentity()
	peers, err := a.be.Peers(ctx)
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
	a.invalidate()
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

// ── The node key ─────────────────────────────────────────────────────────────
//
// The key file IS the identity (backend/identity.go), and a person running a
// player does not know it exists — so the backup is offered here, where the
// consequence is visible, in the words of full-node-mode.md P6: lose it and
// every friendship must be re-paired, every published claim is orphaned.
// One path box serves both acts: a backup writes the key there, a restore
// reads one from there. A restore takes effect at the next start — the node
// keeps the key it came up with — and the line under the box says so until
// the restart happens, on every visit, because a restart is easy to forget.

// nodeKeyRows is the block: the warning, the path box with its two acts, and
// the standing "restart to apply" line when a restore waits.
func (a *App) nodeKeyRows(busy bool) []layout.Widget {
	rows := []layout.Widget{
		func(gtx C) D {
			return layout.Inset{Top: 12}.Layout(gtx, func(gtx C) D {
				l := material.Body2(a.th, "This node's key")
				l.Color = colFg
				return l.Layout(gtx)
			})
		},
		func(gtx C) D {
			return a.sectionHint(gtx,
				"The key file is this node's identity: lose it and every friendship must be "+
					"paired again, and everything this node published is orphaned. Back it up "+
					"to a place you keep. On a fresh install, restoring that file brings the "+
					"node back as itself.")
		},
		func(gtx C) D {
			return a.clipRow(gtx, &a.clipKeyPath, &a.keyPathEd, "~/madplayer-node.key", true,
				layout.Rigid(func(gtx C) D {
					return a.smallButton(gtx, &a.pairing.btnBackUp, "Back up", busy)
				}),
				layout.Rigid(func(gtx C) D {
					return layout.Inset{Left: 6}.Layout(gtx, func(gtx C) D {
						return a.smallButton(gtx, &a.pairing.btnRestore, "Restore", busy)
					})
				}),
			)
		},
	}
	if pending := a.be.PendingKey(); pending != "" {
		rows = append(rows, func(gtx C) D {
			return layout.Inset{Top: 6}.Layout(gtx, func(gtx C) D {
				l := material.Caption(a.th, "A restored key is waiting — restart madplayer to come back as "+
					shortKey(pending)+". Until then this node still runs on the key above.")
				l.Color = colFg
				return l.Layout(gtx)
			})
		})
	}
	return rows
}

// keyAction is pairAction without the editor clear: the path in the box is
// worth keeping — it is where the backup went, or where the next one goes.
func (a *App) keyAction(run func() (string, error)) {
	a.mu.Lock()
	if a.pairing.busy {
		a.mu.Unlock()
		return
	}
	a.pairing.busy = true
	a.mu.Unlock()

	go func() {
		msg, err := run()
		a.mu.Lock()
		a.pairing.busy = false
		if err != nil {
			a.pairing.msg = err.Error()
		} else {
			a.pairing.msg = msg
		}
		a.mu.Unlock()
		a.invalidate()
	}()
}

func (a *App) backUpKey(path string) {
	if strings.TrimSpace(path) == "" {
		a.setPairMsg("Type where to keep the backup — a file name, or a folder to put it in")
		return
	}
	a.keyAction(func() (string, error) {
		dst, err := a.be.BackUpKey(path)
		if err != nil {
			return "", err
		}
		// Remembered, so the node-mode checklist can say a copy exists.
		a.mu.Lock()
		a.cfg.KeyBackup = dst
		cfg := a.cfg
		a.mu.Unlock()
		if err := a.store.Save(cfg); err != nil {
			return "Key backed up to " + dst + " — but the setting did not save: " + err.Error(), nil
		}
		return "Key backed up to " + dst + " — keep that file somewhere safe", nil
	})
}

func (a *App) restoreKey(path string) {
	if strings.TrimSpace(path) == "" {
		a.setPairMsg("Type the path of the key file to restore")
		return
	}
	a.keyAction(func() (string, error) {
		pub, err := a.be.RestoreKey(path)
		if errors.Is(err, backend.ErrSameKey) {
			return "That file holds the key this node already runs on — nothing to restore", nil
		}
		if err != nil {
			return "", err
		}
		return "Key restored. Restart madplayer to come back as " + shortKey(pub) +
			"; the key it ran on until now is kept beside it", nil
	})
}

// setPairMsg writes the section's status line from the UI goroutine.
func (a *App) setPairMsg(s string) {
	a.mu.Lock()
	a.pairing.msg = s
	a.mu.Unlock()
}
