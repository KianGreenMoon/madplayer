package ui

import "testing"

// Node mode is a runtime switch and the paired-nodes page exists exactly while
// it is on. That "exactly" has two halves worth pinning: the page appears when
// the mode is switched on, and a REMEMBERED page falls back to the index when
// the mode is switched off under it — the same rule that retired a stale
// a.settingsPage when the pairing experiment was a const.
func TestNodeModeGatesThePairedNodesPage(t *testing.T) {
	if !nodeModeOffered {
		t.Skip("node mode is not offered in this build")
	}
	a := testApp(t)

	if a.nodeMode() {
		t.Fatal("node mode is on by default — listener is supposed to be the baseline")
	}
	a.openSettingsPage(pagePairing)
	if _, ok := a.openSection(); ok {
		t.Fatal("the paired-nodes page opened with node mode off")
	}
	if d := a.settings(headless()); d.Size.Y == 0 {
		t.Fatal("Settings laid out nothing while falling back to the index")
	}

	a.saveNodeMode(true)
	a.openSettingsPage(pagePairing)
	if _, ok := a.openSection(); !ok {
		t.Fatal("the paired-nodes page is hidden with node mode on")
	}
	if d := a.settings(headless()); d.Size.Y == 0 {
		t.Fatal("the paired-nodes page laid out to nothing")
	}

	a.saveNodeMode(false)
	if _, ok := a.openSection(); ok {
		t.Fatal("switching node mode off left the paired-nodes page reachable")
	}
}

// The switch survives a restart, in both directions. Off is the default AND a
// choice here — unlike Mesh the two coincide, which is what makes omitempty
// safe on the pref — so the assertion is simply that what was saved is read.
func TestNodeModeIsSaved(t *testing.T) {
	if !nodeModeOffered {
		t.Skip("node mode is not offered in this build")
	}
	a := testApp(t)

	a.saveNodeMode(true)
	cfg, err := a.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.NodeMode {
		t.Fatal("node mode on was not written to the settings file")
	}

	a.saveNodeMode(false)
	cfg, err = a.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.NodeMode {
		t.Fatal("node mode off was not written to the settings file")
	}
}

// The index row and the page lay out in both states — the state line is
// computed, and a computed line that panics on the unpaired default would take
// the whole index down with it.
func TestNodeModeControlsLayOut(t *testing.T) {
	if !nodeModeOffered {
		t.Skip("node mode is not offered in this build")
	}
	a := testApp(t)
	for _, on := range []bool{false, true} {
		underLock(a, func() { a.cfg.NodeMode = on })
		if s := a.nodeModeState(); s == "" {
			t.Errorf("node mode (on=%v) says nothing on the index", on)
		}
		if d := a.nodeModeControls(headless()); d.Size.Y == 0 {
			t.Errorf("the node mode page (on=%v) laid out to nothing", on)
		}
	}
}
