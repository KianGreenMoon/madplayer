package ui

import (
	"fmt"
	"net/url"
	"strings"

	"gioui.org/layout"
	"gioui.org/widget"
	"gioui.org/widget/material"
)

// Underlay peers: how this device reaches the mesh when nothing else offers a
// way in.
//
// There are three ways onto the underlay and this is the third. A device
// normally gets there by signing in — a server hands out its peering over
// GET /api/madnetwork/peering and the enrolment loop dials what it gets
// (internal/mesh) — or over the local network, where multicast finds whatever
// is on the wifi. What is left is somebody whose home server publishes no
// peering and whose network has none to discover, and until now that person had
// no way in at all: `prefs.MeshPeers` was read by main.go from the day it was
// written and NOTHING ever set it. That is the same shape as the mesh switch
// before 2026-08-09 — a setting reachable only by hand-editing config.json,
// which is indistinguishable from a setting that does not work.
//
// A typed peer is dialled NOW, not at the next start. madshare's facade grew
// AddPeer for exactly this reason (a device learns where the mesh is by signing
// in, long after startup), and using it here is what makes an address something
// that either connects or says why while its typist is still looking at it. The
// list is saved too, because the dial does not survive a restart and the config
// is what main.go hands the backend on the way up.
//
// Removal is the honest asymmetry: madshare can add a link at runtime and has
// no surface to drop one, so forgetting a peer stops it being dialled at the
// NEXT start and leaves the live link alone. The row says so rather than
// implying a disconnection that did not happen — the same honesty the sign-out
// message owes about a token that stays valid until it is revoked.

// peerSchemes are the URI schemes yggdrasil can dial, from its own dialerFor
// (yggdrasil-go/src/core/link.go). Anything else is refused here rather than
// handed down to be ignored: a peer that never connects and a peer that was
// never understood look identical from the outside, and only one of them is
// worth re-typing.
var peerSchemes = []string{"tls", "tcp", "quic", "ws", "wss", "socks", "sockstls", "unix"}

// peerControls is the peer list and the box that adds to it.
func (a *App) peerControls(gtx C) D {
	a.mu.Lock()
	peers := append([]string(nil), a.cfg.MeshPeers...)
	msg := a.peerMsg
	a.mu.Unlock()

	if a.btnAddPeer.Clicked(gtx) {
		a.addPeer(a.peerEd.Text())
	}
	for len(a.rmPeer) < len(peers) {
		a.rmPeer = append(a.rmPeer, widget.Clickable{})
	}
	for i := range peers {
		if a.rmPeer[i].Clicked(gtx) {
			a.removePeer(peers[i])
		}
	}

	rows := []layout.Widget{
		func(gtx C) D { return a.sectionTitle(gtx, "Peers") },
		func(gtx C) D {
			return a.sectionHint(gtx, peerBlurb(len(peers)))
		},
		func(gtx C) D {
			return a.clipRow(gtx, &a.clipPeer, &a.peerEd, "tls://example.org:7743", true,
				layout.Rigid(func(gtx C) D {
					return a.actionButton(gtx, &a.btnAddPeer, iconAddPeer, false)
				}),
			)
		},
	}
	if msg != "" {
		rows = append(rows, func(gtx C) D {
			return layout.Inset{Top: 10}.Layout(gtx, func(gtx C) D {
				l := material.Caption(a.th, msg)
				l.Color = colDim
				return l.Layout(gtx)
			})
		})
	}
	for i, p := range peers {
		rows = append(rows, a.peerLine(i, p))
	}

	return layout.Inset{Top: 18}.Layout(gtx, func(gtx C) D {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, rigidAll(rows)...)
	})
}

// peerBlurb says what this list is for, and says "usually empty" out loud —
// somebody reading a settings page with nothing in it deserves to know that is
// the intended state rather than something they failed to fill in.
func peerBlurb(n int) string {
	s := "Addresses of nodes to connect to directly, dialled as soon as you add one. " +
		"Usually empty: this device normally reaches the madnetwork through a server it is " +
		"signed in to, or over the local network. Type one here when it has neither — " +
		"tls://host:port, or tcp, quic, ws, wss."
	if n > 0 {
		s += fmt.Sprintf(" %s typed on this device.", plural(n, "peer"))
	}
	return s
}

// peerLine is one configured peer and the button that forgets it.
func (a *App) peerLine(i int, uri string) layout.Widget {
	return func(gtx C) D {
		return layout.Inset{Top: 8}.Layout(gtx, func(gtx C) D {
			return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
				layout.Flexed(1, func(gtx C) D {
					l := material.Body2(a.th, uri)
					l.Color = colFg
					l.MaxLines = 1
					return l.Layout(gtx)
				}),
				layout.Rigid(func(gtx C) D {
					return a.actionButton(gtx, &a.rmPeer[i], iconDelete, false)
				}),
			)
		})
	}
}

// checkPeer is the shape check, and it answers in a sentence.
//
// It refuses what yggdrasil would refuse and nothing more: the schemes it can
// dial, and an address to dial. Everything past that — whether the host exists,
// whether it answers, whether it will accept us — is the dial's answer, not
// this one's, and guessing at it here would only produce a second opinion that
// can disagree with the network.
func checkPeer(raw string) (string, error) {
	uri := strings.TrimSpace(raw)
	if uri == "" {
		return "", fmt.Errorf("type a peer address, like tls://example.org:7743")
	}
	u, err := url.Parse(uri)
	if err != nil {
		return "", fmt.Errorf("%q is not an address: %w", uri, err)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme == "" {
		return "", fmt.Errorf("%q has no protocol in front — try tls://%s", uri, uri)
	}
	known := false
	for _, s := range peerSchemes {
		if scheme == s {
			known = true
			break
		}
	}
	if !known {
		return "", fmt.Errorf("the madnetwork cannot dial %q — it speaks %s",
			scheme, strings.Join(peerSchemes, ", "))
	}
	// unix is a socket on this machine, so it carries a path and no host; every
	// other scheme is a machine somewhere and needs one.
	if scheme == "unix" {
		if u.Path == "" && u.Opaque == "" {
			return "", fmt.Errorf("a unix peer needs a socket path")
		}
		return uri, nil
	}
	if u.Host == "" {
		return "", fmt.Errorf("%q names no host to connect to", uri)
	}
	if u.Port() == "" {
		return "", fmt.Errorf("%q names no port — a peer address ends in one, like %s:7743", uri, u.Host)
	}
	return uri, nil
}

// addPeer records a peer and dials it.
//
// The order is deliberate: SAVED first, then dialled. A dial that fails is
// still a peer worth keeping — the host may be down this minute and up the
// next, and a setting that deletes itself because the network was unavailable
// is a setting nobody can rely on. The message says which of the two happened.
func (a *App) addPeer(raw string) {
	uri, err := checkPeer(raw)
	if err != nil {
		a.setPeerMsg(err.Error())
		return
	}

	a.mu.Lock()
	for _, p := range a.cfg.MeshPeers {
		if p == uri {
			a.mu.Unlock()
			a.setPeerMsg("That peer is already on the list")
			return
		}
	}
	a.cfg.MeshPeers = append(a.cfg.MeshPeers, uri)
	cfg := a.cfg
	a.mu.Unlock()
	a.peerEd.SetText("")

	go func() {
		if err := a.store.Save(cfg); err != nil {
			a.setPeerMsg("could not save the peer: " + err.Error())
			return
		}
		dialled, err := a.be.AddPeer(uri)
		switch {
		case err != nil:
			a.setPeerMsg("Saved, but it could not be dialled: " + err.Error())
		case !dialled:
			a.setPeerMsg("Saved. It is dialled when the madnetwork is on and madplayer restarts")
		default:
			a.setPeerMsg("Connecting to " + uri + "…")
		}
	}()
}

// removePeer forgets one. The live link is madshare's to drop and it has no
// surface for it, so this is honest about lasting until the next start rather
// than reporting a disconnection that did not happen.
func (a *App) removePeer(uri string) {
	a.mu.Lock()
	kept := a.cfg.MeshPeers[:0]
	for _, p := range a.cfg.MeshPeers {
		if p != uri {
			kept = append(kept, p)
		}
	}
	a.cfg.MeshPeers = kept
	cfg := a.cfg
	a.mu.Unlock()

	go func() {
		if err := a.store.Save(cfg); err != nil {
			a.setPeerMsg("could not save: " + err.Error())
			return
		}
		a.setPeerMsg("Removed " + uri + " — an open connection to it lasts until madplayer restarts")
	}()
}

// setPeerMsg writes the section's own status line. It is the section's and not
// the player bar's for the same reason the cache page keeps its own: somebody
// who just typed an address is looking at the box they typed it into.
func (a *App) setPeerMsg(msg string) {
	a.mu.Lock()
	a.peerMsg = msg
	a.mu.Unlock()
	a.win.Invalidate()
}
