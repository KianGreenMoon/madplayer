// Package autostart registers madplayer to start at login, in the tray.
//
// The other half of node mode's presence (madshare docs/plans/full-node-mode.md
// P5): a member that only exists while somebody remembers to start it is a
// member the network keeps writing off. The mechanism is each desktop's own —
// an entry in $XDG_CONFIG_HOME/autostart on the freedesktop platforms, the
// per-user Run key in the registry on Windows — and its truth is what the
// desktop reads: Enabled looks there, so the switch cannot disagree with what
// the desktop will do. The entry passes --hidden, so a login brings the node
// up in the tray and not a window over whatever the person was about to do
// (where no tray host shows the icon, the window opens instead — never a
// node nobody can reach).
//
// Unregistering is as easy as registering (owner's rule): Disable removes the
// one entry Enable wrote, and nothing else is touched. A platform with no
// login story here (macOS, a phone) reports Offered false, and the switch is
// not shown.
package autostart

import (
	"os"
	"path/filepath"
	"strings"
)

// Name is the entry's name on every platform: the XDG file's base name, the
// registry value's name. The desktop matches the file name against the
// launcher entry of the same name, which is how a login-started program keeps
// the launcher's icon.
const Name = "madplayer"

// executable is the running program as the entry should name it, resolved
// through symlinks so the entry survives a PATH that differs at login.
func executable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return exe, nil
}

// command is the entry's command line: the quoted executable, hidden.
func command(exe string) string { return `"` + exe + `" --hidden` }

// exeOf is command's inverse: the executable out of a stored command line.
func exeOf(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	cmd = strings.TrimSuffix(cmd, " --hidden")
	return strings.Trim(cmd, `"`)
}
