//go:build windows

package autostart

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The Run value round trip, on the real per-user key: written for THIS
// program, read back as enabled with its path, removed again. Left as it was
// found — a developer's own entry, if any, is restored.
func TestRunValueRoundTrip(t *testing.T) {
	hadOn, hadExe := Enabled()
	t.Cleanup(func() {
		if !hadOn {
			_ = Disable()
			return
		}
		if _, err := Enable(); err != nil {
			t.Logf("could not restore the entry that pointed at %s: %v", hadExe, err)
		}
	})
	if _, err := Enable(); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	exe, _ := os.Executable()
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	on, got := Enabled()
	if !on || !strings.EqualFold(got, exe) {
		t.Fatalf("Enabled = %v, %q; want true, %q", on, got, exe)
	}
	if err := Disable(); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	if on, _ := Enabled(); on {
		t.Fatal("still enabled after Disable")
	}
	if err := Disable(); err != nil {
		t.Fatalf("disabling twice should be fine: %v", err)
	}
}
