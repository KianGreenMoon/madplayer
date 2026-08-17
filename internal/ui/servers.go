package ui

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"gioui.org/layout"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"daemonlord.ygg/madplayer/internal/madshare"
	"daemonlord.ygg/madplayer/internal/mesh"
	"daemonlord.ygg/madplayer/internal/prefs"
)

// The servers panel: sign in to somebody else's madshare, and say how much of
// their music this device is willing to keep on disk.
//
// There is no "your account" section here and there will not be one for the
// LOCAL library, because there is no local account (docs/design.md §"There
// is no local account"). What appears once signed in belongs to the REMOTE
// server: a username this device authenticates as, and a token that server can
// revoke. Shipping the web UI's settings page wholesale would ask a person to
// manage credentials for a database on their own phone that nothing else can
// reach.

func (a *App) serversPanel(gtx C) D {
	a.mu.Lock()
	servers := append([]prefs.Server(nil), a.cfg.Servers...)
	msg, busy := a.srvMsg, a.srvBusy
	a.mu.Unlock()

	if len(a.rmServer) < len(servers) {
		a.rmServer = append(a.rmServer, make([]widget.Clickable, len(servers)-len(a.rmServer))...)
	}
	for i := range servers {
		if i < len(a.rmServer) && a.rmServer[i].Clicked(gtx) {
			a.signOut(servers[i])
		}
	}
	if a.btnSignIn.Clicked(gtx) && !busy {
		a.signIn(a.srvAddr.Text(), a.srvUser.Text(), a.srvPass.Text())
	}

	return layout.Inset{Top: 16, Bottom: 16, Left: 20, Right: 20}.Layout(gtx, func(gtx C) D {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx C) D { return a.sectionTitle(gtx, "Servers") }),
			layout.Rigid(func(gtx C) D {
				return a.sectionHint(gtx, "Sign in to a madshare to browse its library alongside your own. "+
					"Its tracks are downloaded as they play, and kept in the cache below.")
			}),

			layout.Rigid(func(gtx C) D { return a.signInForm(gtx, busy) }),
			layout.Rigid(func(gtx C) D {
				if msg == "" {
					return D{}
				}
				return layout.Inset{Top: 10}.Layout(gtx, func(gtx C) D {
					l := material.Caption(a.th, msg)
					l.Color = colDim
					return l.Layout(gtx)
				})
			}),

			layout.Rigid(layout.Spacer{Height: 16}.Layout),
			layout.Flexed(1, func(gtx C) D {
				if len(servers) == 0 {
					return a.emptyState(gtx, "Not signed in to any server. Your own music plays without one.")
				}
				return material.List(a.th, &a.serverList).Layout(gtx, len(servers), func(gtx C, i int) D {
					return a.serverRow(gtx, servers[i], &a.rmServer[i])
				})
			}),
		)
	})
}

func (a *App) signInForm(gtx C, busy bool) D {
	field := func(ed *widget.Editor, hint string, weight float32) layout.FlexChild {
		return layout.Flexed(weight, func(gtx C) D {
			e := material.Editor(a.th, ed, hint)
			e.Color, e.HintColor = colFg, colDim
			return filled(gtx, colSel, e.Layout)
		})
	}
	return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
		field(&a.srvAddr, "music.example or 192.168.1.5:3000", 2),
		layout.Rigid(layout.Spacer{Width: 8}.Layout),
		field(&a.srvUser, "username", 1),
		layout.Rigid(layout.Spacer{Width: 8}.Layout),
		field(&a.srvPass, "password", 1),
		layout.Rigid(layout.Spacer{Width: 8}.Layout),
		layout.Rigid(func(gtx C) D {
			label := "Sign in"
			if busy {
				label = "Signing in…"
			}
			return a.smallButton(gtx, &a.btnSignIn, label, busy)
		}),
	)
}

func (a *App) serverRow(gtx C, s prefs.Server, rm *widget.Clickable) D {
	return layout.Inset{Top: 6, Bottom: 6}.Layout(gtx, func(gtx C) D {
		return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
			layout.Flexed(1, func(gtx C) D {
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Rigid(func(gtx C) D {
						l := material.Body2(a.th, serverLabel(s))
						l.Color = colFg
						l.MaxLines = 1
						return l.Layout(gtx)
					}),
					layout.Rigid(func(gtx C) D {
						l := material.Caption(a.th, fmt.Sprintf("signed in as %s · %s", s.Username, s.Base))
						l.Color = colDim
						l.MaxLines = 1
						return l.Layout(gtx)
					}),
				)
			}),
			layout.Rigid(func(gtx C) D { return a.smallButton(gtx, rm, "Sign out", false) }),
		)
	})
}

// meshControls is the madnetwork switch and what it is currently doing.
//
// It exists because the switch was otherwise unreachable: prefs.Mesh and
// backend.Options honoured it from the day they were written, and nothing ever
// set them, so the only way to join the mesh was to hand-edit config.json. A
// feature nobody can turn on is indistinguishable from one that does not work.
//
// The toggle needs a RESTART, and says so rather than pretending. Whether this
// device is a node is decided in the config the backend is built from, and there
// is deliberately no way to turn it on later — two ways to become a node could
// disagree about whether this one is.
func (a *App) meshControls(gtx C) D {
	if a.meshOn.Update(gtx) {
		a.saveMesh(a.meshOn.Value)
	}

	problem := a.be.MeshProblem()
	_, up := a.be.Mesh()
	var rounds []mesh.Status
	if a.enrol != nil {
		rounds = a.enrol.Status()
	}

	return layout.Inset{Top: 16}.Layout(gtx, func(gtx C) D {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx C) D { return a.sectionTitle(gtx, "The madnetwork") }),
			layout.Rigid(func(gtx C) D {
				return a.sectionHint(gtx,
					"Fetch tracks from whoever has them — your other devices and the servers you are "+
						"signed in to — instead of always from the server that listed them, and share "+
						"what you have fetched back. Your own music folders are never shared. "+
						"Takes effect when madplayer restarts.")
			}),
			layout.Rigid(func(gtx C) D {
				cb := material.CheckBox(a.th, &a.meshOn, "Use the madnetwork")
				cb.Color, cb.IconColor = colFg, colFg
				return cb.Layout(gtx)
			}),
			layout.Rigid(func(gtx C) D {
				// One line saying what is actually true right now. The cases are
				// different problems with different answers: a switch that is off, a
				// switch that is on and could not be honoured, and a node that is up
				// but has not yet been vouched for by anybody.
				var txt string
				switch {
				case problem != "":
					txt = problem
				case up:
					txt = meshRoundsText(rounds)
				case a.meshOn.Value:
					txt = "Off until madplayer restarts."
				default:
					txt = "Off. Tracks are downloaded from the server that listed them."
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

// meshRoundsText summarises enrolment for the person who flipped the switch.
//
// A node that is up is not yet a node that can fetch: it needs a vouch from a
// home server, and that is the thing most likely to be missing (no server signed
// in to, or one that has not answered yet). Saying "on" without saying that would
// be the same silence the switch itself used to have.
func meshRoundsText(rounds []mesh.Status) string {
	if len(rounds) == 0 {
		return "On, but no server is signed in to — a device is vouched for by a server, so " +
			"tracks still come from wherever they are listed."
	}
	var enrolled, peers, advertised int
	var problem string
	for _, r := range rounds {
		if !r.Enrolled.IsZero() {
			enrolled++
		}
		if r.Problem != "" && problem == "" {
			problem = r.Problem
		}
		peers += r.Peers
		advertised += r.Advertised
	}
	if enrolled == 0 {
		if problem != "" {
			return "On, but not vouched for yet: " + problem
		}
		return "On. Waiting for a server to vouch for this device…"
	}
	return fmt.Sprintf("On — vouched for by %d of %d server(s), %d peer(s), sharing %d downloaded track(s).",
		enrolled, len(rounds), peers, advertised)
}

func (a *App) sectionTitle(gtx C, txt string) D {
	l := material.Body1(a.th, txt)
	l.Color = colFg
	return l.Layout(gtx)
}

func (a *App) sectionHint(gtx C, txt string) D {
	return layout.Inset{Top: 4, Bottom: 12}.Layout(gtx, func(gtx C) D {
		l := material.Caption(a.th, txt)
		l.Color = colDim
		return l.Layout(gtx)
	})
}

// --- actions ----------------------------------------------------------------

// signIn exchanges a password for a token and saves the server.
//
// The password is never stored and is cleared from the field the moment it has
// been spent — what survives is a token that server lists by name and can
// revoke (internal/madshare.SignIn).
func (a *App) signIn(addr, user, pass string) {
	base, err := madshare.NormalizeBase(addr)
	if err != nil {
		a.setServerMsg(err.Error())
		return
	}
	if strings.TrimSpace(user) == "" || pass == "" {
		a.setServerMsg("type the username and password for that server")
		return
	}

	a.mu.Lock()
	a.srvBusy = true
	a.srvMsg = "Signing in to " + base + "…"
	a.mu.Unlock()
	a.win.Invalidate()

	go func() {
		token, id, err := madshare.New(base, "").SignIn(context.Background(), user, pass)

		a.mu.Lock()
		a.srvBusy = false
		if err != nil {
			a.srvMsg = signInMessage(base, err)
			a.mu.Unlock()
			a.win.Invalidate()
			return
		}
		a.cfg.SetServer(prefs.Server{Base: base, Username: id.Username, Token: token})
		cfg := a.cfg
		// An account without content.access still browses — the server narrows
		// the listing rather than refusing — so an empty library is never
		// evidence that signing in failed. Say which happened.
		a.srvMsg = fmt.Sprintf("Signed in to %s as %s", base, id.Username)
		if !id.Has("content.access") {
			a.srvMsg += " — this account may only play what that server marks guest-playable"
		}
		a.mu.Unlock()

		if err := a.store.Save(cfg); err != nil {
			a.setServerMsg("signed in, but the credential could not be saved: " + err.Error())
		}
		a.srvPass.SetText("")
		a.srvAddr.SetText("")
		a.srvUser.SetText("")
		a.applyServers()
		a.reload()
	}()
}

// signInMessage turns a refusal into the sentence that says what to do next.
func signInMessage(base string, err error) string {
	switch {
	case errors.Is(err, madshare.ErrBadCredentials):
		return "That username and password were not accepted by " + base
	case errors.Is(err, madshare.ErrPasswordChangeRequired):
		return "This account must change its password on " + base + " before it can be used here"
	}
	return "Could not sign in to " + base + ": " + err.Error()
}

// signOut forgets a server on THIS device. The token stays valid until it is
// revoked on the server, where it is listed by name — said out loud, because a
// person signing out reasonably assumes otherwise.
func (a *App) signOut(s prefs.Server) {
	a.mu.Lock()
	a.cfg.RemoveServer(s.Base)
	cfg := a.cfg
	a.srvMsg = "Signed out of " + serverLabel(s) +
		" — the token is still listed on that server until you revoke it there"
	a.mu.Unlock()

	go func() {
		if err := a.store.Save(cfg); err != nil {
			a.setServerMsg("could not save: " + err.Error())
		}
		a.applyServers()
		a.reload()
	}()
}

// saveCacheLimit records the ceiling in the embedded backend and applies it to
// this device's cache.
//
// The number lives in madshare's settings, not in this client's config file, so
// it is the same one a server's settings card writes — one policy, whichever
// surface set it. What differs is the enforcer: madshare sweeps the swarm's
// cache, and this sweeps its own downloads.
// saveCacheLimit records the ceiling in the embedded backend and applies it to
// this device's cache.
//
// Three states, as everywhere else this setting appears: an EMPTY box clears the
// override and falls back to this client's default, a number pins it, and 0 pins
// "no limit". Empty and 0 are different answers and must stay so.
func (a *App) saveCacheLimit(text string) {
	var override *int64
	if s := strings.TrimSpace(text); s != "" {
		mb, err := strconv.Atoi(s)
		if err != nil || mb < 0 {
			a.setServerMsg("type a whole number of MiB, 0 for no limit, or leave it empty for the default")
			return
		}
		n := int64(mb) << 20
		override = &n
	}

	go func() {
		ctx := context.Background()
		if err := a.be.SetCacheCeiling(ctx, override); err != nil {
			a.setServerMsg("could not save the download limit: " + err.Error())
			return
		}
		// Re-read rather than computing it here: the backend owns the resolution
		// of override-over-default, and a second copy of that rule in the UI is
		// how the two come to disagree.
		ceiling, err := a.be.CacheCeiling(ctx)
		if err != nil {
			a.setServerMsg("saved, but the limit could not be read back: " + err.Error())
			return
		}
		a.setCeiling(ceiling)
		if a.cache != nil {
			// Applied now, not at the next download: a person who has just
			// lowered the limit expects the disk back.
			a.cache.SetLimit(ceiling.Effective)
		}
		switch {
		case ceiling.Effective == 0:
			a.setServerMsg("Downloads are no longer limited")
		case override == nil:
			a.setServerMsg("Using the default download limit, " + human(ceiling.Effective))
		default:
			a.setServerMsg("Download limit saved")
		}
		a.refreshCacheSize()
	}()
}

// saveMesh records the switch. Nothing else happens now, by design: the backend
// was built with the old answer and keeps it until the next launch.
func (a *App) saveMesh(on bool) {
	a.mu.Lock()
	a.cfg.Mesh = on
	cfg := a.cfg
	a.mu.Unlock()
	if err := a.store.Save(cfg); err != nil {
		a.setServerMsg("could not save the madnetwork setting: " + err.Error())
		return
	}
	if on {
		a.setServerMsg("The madnetwork is on from the next start of madplayer")
		return
	}
	a.setServerMsg("The madnetwork is off from the next start of madplayer")
}

func (a *App) clearCache() {
	go func() {
		if a.cache == nil {
			return
		}
		if err := a.cache.Clear(); err != nil {
			a.setServerMsg("could not empty the cache: " + err.Error())
			return
		}
		a.setServerMsg("Downloaded music removed — it will be fetched again when played")
		a.refreshCacheSize()
	}()
}

// refreshCacheSize re-measures the cache directory. It walks the disk, so it is
// done on demand rather than per frame.
func (a *App) refreshCacheSize() {
	if a.cache == nil {
		return
	}
	n, err := a.cache.Size()
	if err != nil {
		return
	}
	a.mu.Lock()
	a.cacheUsed = n
	a.mu.Unlock()
	a.win.Invalidate()
}

func (a *App) setServerMsg(msg string) {
	a.mu.Lock()
	a.srvMsg = msg
	a.mu.Unlock()
	a.win.Invalidate()
}

// ceilingText names the limit, or says there is none — "of 0" would read as a
// cache that may hold nothing, which is the opposite of what 0 means.
func ceilingText(limit int64) string {
	if limit <= 0 {
		return "no limit"
	}
	return human(limit)
}

// human renders a byte count the way a person reads a disk.
func human(n int64) string {
	switch {
	case n <= 0:
		return "nothing"
	case n < 1<<20:
		return fmt.Sprintf("%d KiB", n>>10)
	case n < 1<<30:
		return fmt.Sprintf("%d MiB", n>>20)
	}
	return fmt.Sprintf("%.1f GiB", float64(n)/float64(1<<30))
}
