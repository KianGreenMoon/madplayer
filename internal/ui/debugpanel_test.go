package ui

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"daemonlord.ygg/madplayer/internal/logbuf"
)

// The layout and navigation of the Debugging page ride the table tests in
// settingsnav_test.go like every other page. What is pinned here is the part
// a table cannot see: Save must leave a real file holding what was logged,
// because that file is the whole reason the button exists.
func TestSaveWritesTheLogToAFile(t *testing.T) {
	logbuf.Install()
	log.Print("debugpanel test: a line the file must carry")

	a := testApp(t)
	a.openSettingsPage(pageDebug)
	a.btnLogSave.Click()
	a.settings(headless())

	dir := filepath.Join(a.be.DataDir(), "logs")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("no logs dir after Save: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("logs dir holds %d files after one Save, want 1", len(entries))
	}
	b, err := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	if !strings.Contains(text, "a line the file must carry") {
		t.Errorf("saved file does not carry the logged line:\n%s", text)
	}
	if !strings.HasPrefix(text, a.build.BuildLine()) {
		t.Errorf("saved file does not open with the build line:\n%s", text)
	}
}

// The tone is the page's one control that makes sound rather than reading it,
// and the whole reason it exists is that somebody presses it and listens. So
// what is pinned is that pressing it says what is playing — on the screen for
// the person, and in the log, because the answer to "did it crackle?" is
// worthless a day later without a line saying what was playing when.
func TestTheTestToneAnnouncesItself(t *testing.T) {
	logbuf.Install()

	a := testApp(t)
	a.openSettingsPage(pageDebug)
	a.btnTestTone.Click()
	a.settings(headless())

	if !strings.Contains(a.notice, "1 kHz") {
		t.Errorf("notice after the tone is %q, want it to say what is playing", a.notice)
	}
	var found bool
	for _, line := range logbuf.Snapshot() {
		if strings.Contains(line, "test tone") {
			found = true
		}
	}
	if !found {
		t.Error("the log carries no line for the test tone")
	}
}
