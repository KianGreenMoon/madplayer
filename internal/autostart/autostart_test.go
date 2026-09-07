//go:build linux || freebsd || openbsd || netbsd || dragonfly

package autostart

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnableWritesTheEntryAndDisableRemovesIt(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if on, _ := Enabled(); on {
		t.Fatal("enabled before anything was written")
	}
	if err := Disable(); err != nil {
		t.Fatalf("disabling nothing should be fine: %v", err)
	}

	p, err := Enable()
	if err != nil {
		t.Fatalf("Enable: %v", err)
	}
	want, _ := Path()
	if p != want || filepath.Base(p) != Name+".desktop" {
		t.Fatalf("wrote %s, want %s", p, want)
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	exe, _ := os.Executable()
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	if !strings.Contains(string(raw), "Exec=\""+exe+"\" --hidden\n") {
		t.Fatalf("the entry must start THIS program hidden:\n%s", raw)
	}
	if !strings.HasPrefix(string(raw), "[Desktop Entry]\n") {
		t.Fatalf("not a desktop entry:\n%s", raw)
	}
	on, got := Enabled()
	if !on || got != exe {
		t.Fatalf("Enabled = %v, %q; want true, %q", on, got, exe)
	}

	if err := Disable(); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	if on, _ := Enabled(); on {
		t.Fatal("still enabled after Disable")
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("the entry is still on disk: %v", err)
	}
}
