package ui

import (
	"context"
	"fmt"

	"gioui.org/layout"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"daemonlord.ygg/madplayer/internal/backend"
)

// The cache page: what this device is keeping, and how to stop.
//
// It exists as a page rather than a paragraph in Settings because there are
// TWO caches and they answer different questions, which one line of prose kept
// getting wrong. A swarm fetch stores every blob twice — madshare's
// `cache/madnetwork/`, which is the only thing this device SEEDS from, and this
// client's `remote/`, which is what it PLAYS from and the only one with a file
// extension the decoders can read. Until 2026-08-15 the button in Settings said
// "Empty now" and emptied the second; the first, which is the half that grows
// with everything the mesh sends you, stayed.
//
// One ceiling governs both, separately — madshare's own setting, read through
// the facade so there is no second copy to disagree with it. That means the
// worst case on disk is twice the number in the box, and a page about disk
// space has to say so rather than let somebody discover it.
//
// What is NOT here is music kept on purpose (Settings §"Music kept from the
// network"). Those are files a person asked for, in a folder they chose; a
// button that swept them up with the caches would be the worst bug this program
// could have.
//
// Also not here: anybody ELSE's cache. Both directories on this page are under
// this device's own data dir. A server's cache belongs to whoever administers
// that server — clearing it from a client is remote administration, a different
// feature with a different permission behind it, and not something a settings
// page should quietly be (owner, 2026-08-16).

func (a *App) cachePanel(gtx C) D {
	a.mu.Lock()
	played, seedN, seeded := a.cacheUsed, a.seedCount, a.seedUsed
	ceiling, busy, msg := a.ceiling, a.clearing, a.cacheMsg
	a.mu.Unlock()

	if a.btnCacheSave.Clicked(gtx) {
		a.saveCacheLimit(a.cacheEd.Text())
	}
	if a.btnClearPlayed.Clicked(gtx) && !busy {
		a.clearCache()
	}
	if a.btnClearSeeded.Clicked(gtx) && !busy {
		a.clearSeeded()
	}
	if a.btnClearAll.Clicked(gtx) && !busy {
		a.clearAllCaches()
	}

	rows := []layout.Widget{
		func(gtx C) D { return a.sectionTitle(gtx, "Storage") },
		func(gtx C) D {
			return a.sectionHint(gtx, fmt.Sprintf(
				"Music downloaded from a server or the madnetwork is kept here so playing it "+
					"again needs no network, and everything below can be removed at any time — "+
					"it is a cache, so anything cleared is fetched again when you next play it. "+
					"Using %s in total.", human(played+seeded)))
		},
		func(gtx C) D {
			return layout.Inset{Top: 10, Bottom: 4}.Layout(gtx, func(gtx C) D {
				return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx C) D {
						return a.smallButton(gtx, &a.btnClearAll, clearLabel(busy, "Empty every cache"), busy)
					}),
					layout.Rigid(layout.Spacer{Width: 12}.Layout),
					layout.Rigid(func(gtx C) D {
						if msg == "" {
							return D{}
						}
						l := material.Caption(a.th, msg)
						l.Color = colDim
						return l.Layout(gtx)
					}),
				)
			})
		},

		func(gtx C) D { return a.cacheSection(gtx, playbackCache(played, ceiling), &a.btnClearPlayed, busy) },
		func(gtx C) D {
			// Shown whenever there is something to show, rather than whenever the
			// mesh is on. Turning the madnetwork off does not delete what it
			// already fetched, and those bytes are exactly the ones somebody goes
			// looking for — a section that hides with the switch would hide the
			// disk it left behind.
			if seedN == 0 {
				return D{}
			}
			return a.cacheSection(gtx, seededCache(seedN, seeded), &a.btnClearSeeded, busy)
		},

		func(gtx C) D { return a.cacheLimit(gtx, ceiling) },
		func(gtx C) D { return a.keptNote(gtx) },
	}

	return layout.Inset{Top: 16, Bottom: 16, Left: 20, Right: 20}.Layout(gtx, func(gtx C) D {
		lst := material.List(a.th, &a.cacheList)
		lst.Indicator.Color = colLine
		return lst.Layout(gtx, len(rows), func(gtx C, i int) D { return rows[i](gtx) })
	})
}

// cacheFacts is one cache in the words a person needs: what it is for, what it
// weighs, and what clearing it costs.
type cacheFacts struct {
	title string
	size  string
	what  string
	cost  string
}

func playbackCache(used int64, c backend.Ceiling) cacheFacts {
	return cacheFacts{
		title: "Downloaded music",
		size:  human(used),
		what: "The tracks you have played from a server or the madnetwork, ready to play again " +
			"with no network at all. The oldest go first when the limit below is reached.",
		cost: "Clearing it costs a download the next time you play one of them.",
	}
}

func seededCache(count int, used int64) cacheFacts {
	return cacheFacts{
		title: "Shared with the madnetwork",
		size:  fmt.Sprintf("%s in %s", human(used), plural(count, "track")),
		what: "What this device fetched over the madnetwork and now offers back to your other " +
			"devices and the servers you are signed in to. It is the only thing shared from " +
			"here — your own music folders never are.",
		cost: "Clearing it gives the space back and stops this device holding those tracks " +
			"for anybody else. Nothing is lost: whoever you fetched them from still has them.",
	}
}

func (a *App) cacheSection(gtx C, f cacheFacts, click *widget.Clickable, busy bool) D {
	return layout.Inset{Top: 18}.Layout(gtx, func(gtx C) D {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx C) D {
				return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
					layout.Flexed(1, func(gtx C) D { return a.sectionTitle(gtx, f.title) }),
					layout.Rigid(func(gtx C) D {
						l := material.Body2(a.th, f.size)
						l.Color = colDim
						return l.Layout(gtx)
					}),
					layout.Rigid(layout.Spacer{Width: 12}.Layout),
					layout.Rigid(func(gtx C) D {
						return a.smallButton(gtx, click, clearLabel(busy, "Clear"), false)
					}),
				)
			}),
			layout.Rigid(func(gtx C) D { return a.sectionHint(gtx, f.what+" "+f.cost) }),
		)
	})
}

// cacheLimit is the ceiling, and the sentence that keeps it honest: it is one
// number applied to each cache separately, so the two together can reach twice
// it. Stating that is cheaper than a person measuring their own disk and
// concluding the setting is broken.
func (a *App) cacheLimit(gtx C, c backend.Ceiling) D {
	return layout.Inset{Top: 18}.Layout(gtx, func(gtx C) D {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx C) D { return a.sectionTitle(gtx, "How much to keep") }),
			layout.Rigid(func(gtx C) D {
				return a.sectionHint(gtx, fmt.Sprintf(
					"%s each, enforced on both of the above separately — so together they can "+
						"reach twice it. Leave the box empty for the default (%s), or type 0 for "+
						"no limit.",
					ceilingText(c.Effective), ceilingText(c.Default)))
			}),
			layout.Rigid(func(gtx C) D {
				return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx C) D {
						gtx.Constraints.Max.X = gtx.Dp(120)
						e := material.Editor(a.th, &a.cacheEd, "Default")
						e.Color, e.HintColor = colFg, colDim
						return filled(gtx, colSel, e.Layout)
					}),
					layout.Rigid(layout.Spacer{Width: 8}.Layout),
					layout.Rigid(func(gtx C) D {
						l := material.Caption(a.th, "MiB  (empty = default, 0 = no limit)")
						l.Color = colDim
						return l.Layout(gtx)
					}),
					layout.Rigid(layout.Spacer{Width: 12}.Layout),
					layout.Rigid(func(gtx C) D { return a.smallButton(gtx, &a.btnCacheSave, "Save", false) }),
				)
			}),
		)
	})
}

// keptNote is the boundary of this page, said out loud.
func (a *App) keptNote(gtx C) D {
	a.mu.Lock()
	keeper := a.keeper
	a.mu.Unlock()
	root := ""
	if keeper != nil {
		root = keeper.Root()
	}
	if root == "" {
		return D{}
	}
	return layout.Inset{Top: 18}.Layout(gtx, func(gtx C) D {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx C) D { return a.sectionTitle(gtx, "Not a cache") }),
			layout.Rigid(func(gtx C) D {
				return a.sectionHint(gtx, "Music you kept on purpose lives in "+root+
					" and nothing on this page touches it. Those are your files, in a folder you "+
					"chose; remove them the way you would remove any other music.")
			}),
		)
	})
}

func clearLabel(busy bool, idle string) string {
	if busy {
		return "Clearing…"
	}
	return idle
}

// clearSeeded stops this device seeding what it fetched.
func (a *App) clearSeeded() {
	a.withClearing(func() string {
		freed, err := a.be.ClearSeeded()
		if err != nil {
			return "Some of it would not go: " + err.Error()
		}
		if freed == 0 {
			return "Nothing was being shared"
		}
		return human(freed) + " freed — this device no longer holds those tracks for anybody"
	})
}

// clearAllCaches empties both, in one gesture, because "clear the cache" is one
// thought and having to press two buttons to finish it is how the second one
// gets forgotten.
func (a *App) clearAllCaches() {
	a.withClearing(func() string {
		var freed int64
		var failed string
		if a.cache != nil {
			before, _ := a.cache.Size()
			if err := a.cache.Clear(); err != nil {
				failed = err.Error()
			} else {
				freed += before
			}
		}
		n, err := a.be.ClearSeeded()
		if err != nil && failed == "" {
			failed = err.Error()
		}
		freed += n
		if failed != "" {
			return "Some of it would not go: " + failed
		}
		return human(freed) + " freed"
	})
}

// withClearing runs one clear at a time and reports what it did, on the page
// rather than in the player bar: a person watching a number go down is looking
// here.
func (a *App) withClearing(run func() string) {
	a.mu.Lock()
	if a.clearing {
		a.mu.Unlock()
		return
	}
	a.clearing = true
	a.mu.Unlock()

	go func() {
		msg := run()
		a.mu.Lock()
		a.clearing, a.cacheMsg = false, msg
		a.mu.Unlock()
		// BOTH numbers, or the section that was just emptied goes on showing what
		// it held until somebody reopens the page — which reads as a clear that
		// did nothing. (It did exactly that until the first live run of this
		// page, 2026-08-16.)
		a.refreshCacheSize()
		a.refreshSeedUsage()
		a.win.Invalidate()
	}()
}

// refreshSeedUsage measures the seeded cache. It reads a directory, so it is
// done when the page is opened and after a clear, never per frame.
func (a *App) refreshSeedUsage() {
	count, bytes := a.be.SeedUsage()
	a.mu.Lock()
	a.seedCount, a.seedUsed = count, bytes
	a.mu.Unlock()
}

// reloadCeiling re-reads the limit from the backend, so the page opens showing
// what is actually in force rather than what it last remembered.
func (a *App) reloadCeiling() {
	ceiling, err := a.be.CacheCeiling(context.Background())
	if err != nil {
		return
	}
	a.setCeiling(ceiling)
}
