package ui

import (
	"context"
	"image"
	"io"
	"log"
	"testing"

	"gioui.org/app"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/gopxl/beep/v2"

	"daemonlord.ygg/madplayer/internal/backend"
	"daemonlord.ygg/madplayer/internal/player"
	"daemonlord.ygg/madplayer/internal/prefs"
)

// The Settings panel is the one surface this host cannot click: Gio has no
// input-injection tool here, so a panel reached by a button is never exercised
// by running the program (CLAUDE.md §Gotchas). It gets laid out headlessly
// instead — no window, no frames, no sound card — which is enough to catch the
// failure that matters: a panel that panics the moment somebody opens it.
//
// The madnetwork section is why this exists. It reads the backend's mesh state
// and the enrolment loop's status, and on a device with neither those are a nil
// interface and a nil pointer.

// silentSink is player.Sink with no device behind it.
type silentSink struct{}

func (silentSink) Init(beep.SampleRate, int) error { return nil }
func (silentSink) Play(beep.Streamer)              {}
func (silentSink) Lock()                           {}
func (silentSink) Unlock()                         {}
func (silentSink) Clear()                          {}
func (silentSink) Close() error                    { return nil }

func testApp(t *testing.T) *App {
	t.Helper()
	dir := t.TempDir()
	lg := log.New(io.Discard, "", 0)

	pl, err := player.New(silentSink{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pl.Close() })

	// Mesh off, which is the case that has nil where a node would be — the one a
	// nil check is most likely to be missing from.
	be, err := backend.Open(context.Background(), dir, lg, backend.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(be.Close)

	// The store is named UP FRONT rather than replaced afterwards: by then the
	// saved queue has already been read and the background writer is already
	// pointed at the real settings directory.
	return newApp(new(app.Window), pl, be, &prefs.Store{Dir: dir})
}

// headless is a layout context with no window behind it.
func headless() layout.Context {
	return layout.Context{
		Ops:         new(op.Ops),
		Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Constraints: layout.Exact(image.Pt(1000, 720)),
	}
}

func TestSettingsPanelLaysOut(t *testing.T) {
	a := testApp(t)
	// Twice: the first pass is what a person sees, and the second is where a
	// widget that mutated state during layout would show up.
	for i := 0; i < 2; i++ {
		if d := a.settings(headless()); d.Size.X == 0 && d.Size.Y == 0 {
			t.Fatal("the settings panel laid out to nothing")
		}
	}
}

// The panel is taller than a small window, and everything under the fold has to
// be reachable — the download limit, the madnetwork switch, the keep folder and
// the shortcut list all live down there.
//
// It was a vertical Flex until 2026-08-15, which does not scroll: the folder
// list took every pixel the sections below did not want, and on a window shorter
// than the panel those sections were laid out into whatever was left, which is
// nothing. A person on a half-screen window could not turn the madnetwork on.
func TestTheSettingsPanelScrollsToItsLowerSections(t *testing.T) {
	a := testApp(t)
	short := func() layout.Context {
		gtx := headless()
		gtx.Constraints = layout.Exact(image.Pt(1000, 240))
		return gtx
	}

	a.settings(short())
	if !a.folderList.Position.BeforeEnd {
		t.Fatal("a 240px window fitted the whole settings panel — this test is no longer testing anything")
	}

	// Scroll past the end; a list clamps to its last item.
	a.folderList.Position.First = 1 << 20
	a.settings(short())
	if a.folderList.Position.BeforeEnd {
		t.Error("scrolling to the end of Settings did not reach the end — the lower sections are unreachable")
	}
	if a.folderList.Position.Count == 0 {
		t.Error("the end of Settings laid out nothing")
	}
}

func TestMeshControlsLayOutWithNoNode(t *testing.T) {
	a := testApp(t)
	if a.enrol != nil {
		t.Fatal("a mesh-off backend produced an enrolment loop")
	}
	if d := a.meshControls(headless()); d.Size.Y == 0 {
		t.Fatal("the madnetwork section laid out to nothing")
	}
}

// The switch shows what was ASKED for, not what happened — they differ on a
// device where the mesh could not come up, and the caption beside it explains
// that. A box that silently unticked itself would read as "it did not save".
func TestTheMeshSwitchShowsWhatWasAskedFor(t *testing.T) {
	a := testApp(t)
	a.cfg.Mesh = true
	a.meshOn.Value = true
	a.meshControls(headless())
	if !a.meshOn.Value {
		t.Error("the switch unticked itself on a device where the mesh did not come up")
	}
}

// Saving is what was missing entirely: prefs.Mesh existed from the start and
// nothing ever wrote it, so the only way onto the mesh was to hand-edit
// config.json.
func TestSavingTheMeshSwitchReachesTheConfigFile(t *testing.T) {
	a := testApp(t)
	a.saveMesh(true)

	cfg, err := a.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Mesh {
		t.Fatal("turning the madnetwork on did not reach the settings file")
	}
	a.saveMesh(false)
	if cfg, err = a.store.Load(); err != nil || cfg.Mesh {
		t.Fatalf("turning it off did not reach the settings file (err=%v)", err)
	}
}

// The servers this device is signed in to are worked out and KEPT whether or not
// the mesh exists to receive them.
//
// The bug this pins: applyServers used to skip a nil enrolment silently, and it
// ran before the mesh was built — so the enrolment loop started with an empty
// server list, nothing ever filled it, no capability token was ever fetched, and
// every swarm fetch declined for want of a vouch. Without a word, for a week. The
// madnetwork had never once been used.
func TestTheServerListSurvivesHavingNoMeshToTellYet(t *testing.T) {
	a := testApp(t)
	if a.enrol != nil {
		t.Fatal("a mesh-off backend produced an enrolment loop")
	}

	a.mu.Lock()
	a.cfg.Servers = []prefs.Server{{Base: "https://one.example", Label: "one", Token: "t1"}}
	a.mu.Unlock()
	a.applyServers()

	a.mu.Lock()
	homes := a.homes
	a.mu.Unlock()

	if len(homes) != 1 {
		t.Fatalf("kept %d home server(s), want 1 — a mesh built later would learn nothing", len(homes))
	}
	if homes[0].Base != "https://one.example" || homes[0].Label != "one" {
		t.Errorf("home = %+v", homes[0])
	}
	if homes[0].Client == nil {
		t.Error("the home server has no client, so nothing could ask it for a token")
	}

	// Signing out has to reach the mesh too: it stops this device taking that
	// server's word about strangers.
	a.mu.Lock()
	a.cfg.Servers = nil
	a.mu.Unlock()
	a.applyServers()

	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.homes) != 0 {
		t.Errorf("kept %d home server(s) after signing out of every one", len(a.homes))
	}
}
