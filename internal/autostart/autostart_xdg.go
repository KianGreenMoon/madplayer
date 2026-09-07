//go:build linux || freebsd || openbsd || netbsd || dragonfly

package autostart

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The freedesktop platforms: an entry in $XDG_CONFIG_HOME/autostart, which
// every desktop that has a login session honours.

// Offered reports that a login entry can be written here.
func Offered() bool { return true }

// Path is where the entry lives.
func Path() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "autostart", Name+".desktop"), nil
}

// Enabled reports whether an entry of ours exists — and, when it does, which
// program it starts, since an install that moved leaves an entry pointing at
// nothing.
func Enabled() (on bool, exe string) {
	p, err := Path()
	if err != nil {
		return false, ""
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return false, ""
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if rest, ok := strings.CutPrefix(line, "Exec="); ok {
			return true, exeOf(rest)
		}
	}
	return true, ""
}

// Enable writes the entry for the running program.
func Enable() (string, error) {
	exe, err := executable()
	if err != nil {
		return "", err
	}
	if strings.ContainsAny(exe, "\"\n") {
		return "", fmt.Errorf("the program's path %q cannot be written into a desktop entry", exe)
	}
	p, err := Path()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		return "", err
	}
	entry := "[Desktop Entry]\n" +
		"Type=Application\n" +
		"Name=madplayer\n" +
		"Comment=madplayer's node, started at login in the tray\n" +
		"Exec=" + command(exe) + "\n" +
		"Icon=madplayer\n" +
		"Terminal=false\n" +
		"StartupNotify=false\n" +
		"X-GNOME-Autostart-enabled=true\n"
	if err := os.WriteFile(p, []byte(entry), 0644); err != nil {
		return "", err
	}
	return p, nil
}

// Disable removes the entry. An entry that is already gone is fine.
func Disable() error {
	p, err := Path()
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
