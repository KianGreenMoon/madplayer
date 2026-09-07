// Package autostart registers madplayer to start at login, in the tray.
//
// The other half of node mode's presence (madshare docs/plans/full-node-mode.md
// P5): a member that only exists while somebody remembers to start it is a
// member the network keeps writing off. The mechanism is the freedesktop one
// — an entry in $XDG_CONFIG_HOME/autostart, which every desktop that has a
// login session honours — and its truth is the file: Enabled reads the disk,
// so the switch cannot disagree with what the desktop will do. The entry
// passes --hidden, so a login brings the node up in the tray and not a window
// over whatever the person was about to do.
//
// Unregistering is as easy as registering (owner's rule): Disable removes the
// one file Enable wrote, and nothing else is touched.
package autostart

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Name is the entry's file name; the desktop matches it against the launcher
// entry of the same name, which is how a login-started program keeps the
// launcher's icon.
const Name = "madplayer.desktop"

// Path is where the entry lives.
func Path() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "autostart", Name), nil
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
			rest = strings.TrimSpace(rest)
			rest = strings.TrimSuffix(rest, " --hidden")
			return true, strings.Trim(rest, `"`)
		}
	}
	return true, ""
}

// Enable writes the entry for the running program. The path is the
// executable as it is now, resolved through symlinks, so the entry survives a
// PATH that differs at login.
func Enable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
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
		"Exec=\"" + exe + "\" --hidden\n" +
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
