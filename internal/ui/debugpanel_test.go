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
