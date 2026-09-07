package ui

// Background presence: the program in the desktop's tray while its window is
// closed (madshare docs/plans/full-node-mode.md P5, the client half).
//
// Membership is presence. A node that dies when the player window closes is
// exactly the churny neighbour the availability machinery keeps writing off,
// so a member needs a way to close the window and keep the node — and the
// person needs a way to see that it is still there and to get it back. That
// is the tray icon (internal/tray): a click brings the window, the menu's Quit
// stops everything. Playback keeps going too — a player that fell silent
// because its window closed would be a bug report, and the media keys still
// reach it (mpris).
//
// A closed Gio window is destroyed, so "the window" is a value that comes and
// goes: hide stores nil, show opens a fresh one, and the event loop in Run
// alternates between drawing frames and waiting in the tray. Nothing hides
// unless a tray host actually shows the icon — a program that vanished
// behind an icon nobody displays would be unreachable — which is why the
// switch is honoured through tray.Item.Hosted at the moment of closing, not
// through the setting alone.

import (
	"image"
	"log"
	"os"
	"time"

	"gioui.org/app"
	"gioui.org/layout"
	"gioui.org/unit"
	"gioui.org/widget/material"

	"daemonlord.ygg/madplayer/internal/autostart"
	"daemonlord.ygg/madplayer/internal/icon"
	"daemonlord.ygg/madplayer/internal/tray"
)

// trayStartPatience is how long a --hidden start waits for a tray host to
// show the icon before it opens the window instead — the bar may start after
// this program at login.
const trayStartPatience = 10 * time.Second

// WindowOptions is the window as the program opens it — the first one from
// main, and every one after a return from the tray.
func WindowOptions() []app.Option {
	return []app.Option{
		app.Title("madplayer"),
		app.Size(unit.Dp(1000), unit.Dp(720)),
	}
}

func windowOptions(w *app.Window) {
	if w == nil {
		return
	}
	w.Option(WindowOptions()...)
	// Android paints the system bars itself, and its default is white — a
	// glaring strip over a dark player. Both bars take the bar color: the
	// status bar sits on the header, the navigation bar under the player bar,
	// and those two are the same color already. A no-op on the desktop.
	w.Option(app.StatusColor(colBar), app.NavigationColor(colBar))
}

// StartHidden asks Run to begin in the tray, with no window, when a tray
// host shows the icon. The autostart entry passes --hidden for this.
func (a *App) StartHidden() { a.startHidden = true }

// window is the current window, or nil in the tray.
func (a *App) window() *app.Window { return a.win.Load() }

// invalidate asks for a frame, from any goroutine, window or not.
func (a *App) invalidate() {
	if w := a.win.Load(); w != nil {
		w.Invalidate()
	}
}

// applyTray makes the tray item match the setting: on creates it, off takes
// it down. Idempotent, so the switch and Run can both call it.
func (a *App) applyTray() {
	a.mu.Lock()
	want := a.cfg.Tray
	a.mu.Unlock()
	if !want {
		if it := a.trayItem.Swap(nil); it != nil {
			it.Close()
		}
		return
	}
	if a.trayItem.Load() != nil {
		return
	}
	it, err := tray.New(tray.Options{
		ID: "madplayer", Title: "madplayer", IconName: "madplayer",
		Icons:     []image.Image{icon.Draw(22), icon.Draw(32), icon.Draw(48), icon.Draw(64)},
		ShowLabel: "Show madplayer", QuitLabel: "Quit madplayer",
		OnShow: a.show, OnQuit: a.quit,
	})
	if err != nil {
		log.Printf("madplayer: no tray: %v", err)
		a.mu.Lock()
		a.trayMsg = "No tray on this desktop (" + err.Error() + ") — closing the window quits."
		a.mu.Unlock()
		return
	}
	if old := a.trayItem.Swap(it); old != nil {
		old.Close()
	}
}

// shouldHide is the question the closing window asks: keep the program, or
// end it? Only when the person asked for the tray AND a host shows the icon
// AND nobody asked to quit.
func (a *App) shouldHide() bool {
	if a.quitting.Load() {
		return false
	}
	a.mu.Lock()
	want := a.cfg.Tray
	a.mu.Unlock()
	it := a.trayItem.Load()
	return want && it != nil && it.Hosted()
}

// hide is the window going away with the program staying: the state a quit
// would have written is written now, so a crash in the tray loses nothing.
func (a *App) hide() {
	a.hidden.Store(true)
	a.win.Store(nil)
	a.save()
	a.writeQueue()
	log.Printf("madplayer: window closed — running in the tray")
}

// waitInTray blocks until the tray asks for the window (true) or for the end
// of the program (false).
func (a *App) waitInTray() bool {
	select {
	case <-a.showCh:
		return true
	case <-a.quitCh:
		return false
	}
}

// openWindow is the way back from the tray: a new window, the same options,
// and a title that will be pushed again since the old one died with its
// window.
func (a *App) openWindow() {
	w := new(app.Window)
	windowOptions(w)
	a.title = ""
	a.win.Store(w)
	a.hidden.Store(false)
	log.Printf("madplayer: window back from the tray")
}

// show brings the window back, from any goroutine: the tray's click, the
// media bus's Raise, a second launch. A no-op while a window is open — Gio
// cannot raise one — and never queued for later, or a stale request would
// pop the window right back after the next close.
func (a *App) show() {
	if !a.hidden.Load() {
		return
	}
	select {
	case a.showCh <- struct{}{}:
	default:
	}
}

// quit ends the program from wherever: the tray's Quit, the media bus's Quit.
// With a window open it closes it and the destroy handler sees the flag; in
// the tray it wakes the wait.
func (a *App) quit() {
	a.quitting.Store(true)
	if a.hidden.Load() {
		select {
		case a.quitCh <- struct{}{}:
		default:
		}
		return
	}
	a.closeWindow()
}

// trayHostedWithin waits up to d for a host to show the icon.
func (a *App) trayHostedWithin(d time.Duration) bool {
	deadline := time.Now().Add(d)
	for {
		it := a.trayItem.Load()
		if it == nil {
			return false
		}
		if it.Hosted() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// saveAutostart is the login switch: the entry written or removed, and the
// truth re-read from disk either way.
func (a *App) saveAutostart(on bool) {
	var err error
	if on {
		_, err = autostart.Enable()
	} else {
		err = autostart.Disable()
	}
	a.mu.Lock()
	if err != nil {
		a.autostartMsg = "could not change the login entry: " + err.Error()
	} else {
		a.autostartMsg = ""
	}
	a.mu.Unlock()
	a.autostartOn.Value, _ = autostart.Enabled()
}

// saveTray is the switch: written, then applied.
func (a *App) saveTray(on bool) {
	a.mu.Lock()
	a.cfg.Tray = on
	cfg := a.cfg
	a.trayMsg = ""
	a.mu.Unlock()
	if err := a.store.Save(cfg); err != nil {
		a.mu.Lock()
		a.trayMsg = "could not save the tray setting: " + err.Error()
		a.mu.Unlock()
	}
	a.applyTray()
}

// trayControls is the switch and its one line of truth, under the node-mode
// switch: presence is where the consequence of membership is visible.
func (a *App) trayControls(gtx C) D {
	if !nodeModeOffered {
		return D{}
	}
	a.mu.Lock()
	on, msg := a.cfg.Tray, a.trayMsg
	a.mu.Unlock()
	it := a.trayItem.Load()

	var txt string
	switch {
	case msg != "":
		txt = msg
	case !on:
		txt = "Off. Closing the window quits, node and all."
	case it == nil:
		txt = "On, but there is no session bus to put an icon on — closing the window quits."
	case !it.Hosted():
		txt = "On, but no tray host on this desktop shows the icon — closing the window quits " +
			"until one does."
	default:
		txt = "On. Closing the window keeps the node and the music; the tray icon brings " +
			"the window back, and its Quit stops everything."
	}
	// The login entry's truth is the file, read once per frame is too often
	// and once ever is stale — so on the same cadence as the peer table.
	if time.Since(a.autostartRead) > pairingRefresh {
		a.autostartRead = time.Now()
		a.autostartOn.Value, a.autostartExe = autostart.Enabled()
	}
	if a.autostartOn.Update(gtx) {
		a.saveAutostart(a.autostartOn.Value)
	}
	a.mu.Lock()
	amsg := a.autostartMsg
	a.mu.Unlock()
	var atxt string
	switch {
	case amsg != "":
		atxt = amsg
	case !a.autostartOn.Value:
		atxt = "Off. The node runs only while you start madplayer."
	case a.autostartExe != "" && !exists(a.autostartExe):
		atxt = "On, but the entry starts " + a.autostartExe + ", which is not there any more — " +
			"switch it off and on again from the program you now use."
	case !on:
		atxt = "On: madplayer starts at login. With the tray off it opens its window; " +
			"switch the tray on to have it start quietly."
	default:
		atxt = "On: madplayer starts at login, in the tray."
	}

	return layout.Inset{Top: 16}.Layout(gtx, func(gtx C) D {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx C) D {
				return a.sectionHint(gtx,
					"Membership is presence: a node that stops whenever the window closes is the "+
						"neighbour everyone keeps writing off. With the tray on, closing the "+
						"window keeps this program running — seeding, syncing, and playing.")
			}),
			layout.Rigid(func(gtx C) D {
				cb := material.CheckBox(a.th, &a.trayOn, "Keep running in the tray when the window is closed")
				cb.Color, cb.IconColor = colFg, colFg
				return cb.Layout(gtx)
			}),
			layout.Rigid(func(gtx C) D {
				return layout.Inset{Top: 8}.Layout(gtx, func(gtx C) D {
					l := material.Caption(a.th, txt)
					l.Color = colDim
					return l.Layout(gtx)
				})
			}),
			layout.Rigid(func(gtx C) D {
				return layout.Inset{Top: 12}.Layout(gtx, func(gtx C) D {
					cb := material.CheckBox(a.th, &a.autostartOn, "Start at login")
					cb.Color, cb.IconColor = colFg, colFg
					return cb.Layout(gtx)
				})
			}),
			layout.Rigid(func(gtx C) D {
				return layout.Inset{Top: 8}.Layout(gtx, func(gtx C) D {
					l := material.Caption(a.th, atxt)
					l.Color = colDim
					return l.Layout(gtx)
				})
			}),
		)
	})
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
