package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The checklist on a player with the mesh off — testApp's state — must name
// the mesh as the first thing to do, lay out, and say so on the index.
func TestConnectStepsStartWithTheMesh(t *testing.T) {
	if !nodeModeOffered {
		t.Skip("node mode is not offered in this build")
	}
	a := testApp(t)
	// testApp's settings default the mesh to ON while its backend runs
	// without one — the "switched on, restart pending" state.
	underLock(a, func() { a.cfg.NodeMode = true })
	steps := a.connectSteps()
	if len(steps) != 5 {
		t.Fatalf("%d steps, want 5", len(steps))
	}
	if steps[0].done || !strings.Contains(steps[0].state, "restarts") || steps[0].act != nil {
		t.Fatalf("a mesh switched on but not up should say a restart is owed, with nothing to press: %+v", steps[0])
	}

	underLock(a, func() { a.cfg.Mesh = false })
	steps = a.connectSteps()
	if steps[0].done || !strings.Contains(steps[0].state, "Off") || steps[0].act == nil {
		t.Fatalf("mesh off should be the first undone step with a way to the switch: %+v", steps[0])
	}
	for _, s := range steps[1:3] {
		if s.done || !strings.Contains(s.state, "After the madnetwork") {
			t.Fatalf("with the mesh off, %q should wait for it: %+v", s.title, s)
		}
	}
	if !strings.HasPrefix(a.nodeModeState(), "On · next: the madnetwork") {
		t.Fatalf("index line %q should name the first step", a.nodeModeState())
	}
	if d := a.connectControls(headless()); d.Size.Y == 0 {
		t.Fatal("the checklist laid out to nothing")
	}
	if d := a.nodeModeControls(headless()); d.Size.Y == 0 {
		t.Fatal("the node mode page with the checklist laid out to nothing")
	}
	// Following the act lands on the madnetwork page.
	steps[0].act()
	if a.settingsPage != pageNetwork {
		t.Fatalf("the mesh step's act opened page %v, want the madnetwork", a.settingsPage)
	}
}

// The key step reads the remembered backup, and notices a copy that is gone.
func TestConnectKeyStepFollowsTheBackup(t *testing.T) {
	if !nodeModeOffered {
		t.Skip("node mode is not offered in this build")
	}
	a := testApp(t)
	underLock(a, func() { a.cfg.NodeMode = true })

	key := a.connectSteps()[3]
	if key.done || !strings.Contains(key.state, "Not yet") {
		t.Fatalf("no backup yet: %+v", key)
	}

	p := filepath.Join(t.TempDir(), "node.key")
	if err := os.WriteFile(p, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	underLock(a, func() { a.cfg.KeyBackup = p })
	key = a.connectSteps()[3]
	if !key.done || !strings.Contains(key.state, p) {
		t.Fatalf("a backup on disk should be done and named: %+v", key)
	}

	os.Remove(p)
	key = a.connectSteps()[3]
	if key.done || !strings.Contains(key.state, "not there any more") {
		t.Fatalf("a vanished backup should be noticed: %+v", key)
	}
}

// Presence is the last step, and both switches are needed for it.
func TestConnectPresenceStepNeedsBothSwitches(t *testing.T) {
	if !nodeModeOffered {
		t.Skip("node mode is not offered in this build")
	}
	a := testApp(t)
	underLock(a, func() { a.cfg.NodeMode = true; a.cfg.Tray = true })
	a.mu.Lock()
	a.autostartOn.Value = false
	a.mu.Unlock()
	if s := a.connectSteps()[4]; s.done || !strings.Contains(s.state, "start at login") {
		t.Fatalf("tray alone is not presence: %+v", s)
	}
	a.mu.Lock()
	a.autostartOn.Value = true
	a.mu.Unlock()
	if s := a.connectSteps()[4]; !s.done {
		t.Fatalf("tray + login should be done: %+v", s)
	}
}
