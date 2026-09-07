package ui

import (
	"testing"
	"time"
)

func TestTrayPrefIsSaved(t *testing.T) {
	if !nodeModeOffered {
		t.Skip("the tray is not offered in this build")
	}
	a := testApp(t)
	a.saveTray(true)
	cfg, err := a.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Tray {
		t.Fatal("tray on was not written to the settings file")
	}
	a.saveTray(false)
	if cfg, _ = a.store.Load(); cfg.Tray {
		t.Fatal("tray off was not written to the settings file")
	}
	if a.trayItem.Load() != nil {
		t.Fatal("tray off left an item on the bus")
	}
}

// A closing window hides only behind an icon a host shows. Without an item
// — the setting off, no bus, no host — it must end the program, or the
// program is running with no way to reach it.
func TestHidingNeedsAHostedTray(t *testing.T) {
	a := testApp(t)
	if a.shouldHide() {
		t.Fatal("hides with the tray off")
	}
	a.mu.Lock()
	a.cfg.Tray = true
	a.mu.Unlock()
	if a.trayItem.Load() == nil && a.shouldHide() {
		t.Fatal("hides with the tray on but no item")
	}
	a.quitting.Store(true)
	if a.shouldHide() {
		t.Fatal("hides after a quit was asked for")
	}
}

// show and quit wake the tray wait, and a show with the window open is
// dropped rather than queued for the next close.
func TestShowAndQuitWakeTheTrayWait(t *testing.T) {
	a := testApp(t)

	a.show() // window open: must not queue
	a.hidden.Store(true)
	done := make(chan bool, 1)
	go func() { done <- a.waitInTray() }()
	select {
	case v := <-done:
		t.Fatalf("the wait ended (%v) on a show sent while the window was open", v)
	case <-time.After(100 * time.Millisecond):
	}
	a.show()
	if v := <-done; !v {
		t.Fatal("a show should end the wait with true")
	}

	a.hidden.Store(true)
	go func() { done <- a.waitInTray() }()
	a.quit()
	if v := <-done; v {
		t.Fatal("a quit should end the wait with false")
	}
	if !a.quitting.Load() {
		t.Fatal("quit did not set the flag the closing window reads")
	}
	// And openWindow must leave a window behind, with the title to be pushed.
	a.title = "stale"
	a.openWindow()
	if a.window() == nil || a.hidden.Load() || a.title != "" {
		t.Fatal("openWindow did not restore a window")
	}
	a.hidden.Store(true)
	a.win.Store(nil)
	a.invalidate() // nil-safe
	a.closeWindow()
}
