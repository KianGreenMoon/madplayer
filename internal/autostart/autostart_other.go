//go:build !(linux || freebsd || openbsd || netbsd || dragonfly || windows)

package autostart

import "errors"

// Everywhere else — macOS (LaunchAgents are not written yet), phones (no
// login to speak of) — there is no entry to write, and the switch is not
// shown.

// Offered reports that no login entry can be written here.
func Offered() bool { return false }

var errNotOffered = errors.New("starting at login is not offered on this platform")

func Path() (string, error)          { return "", errNotOffered }
func Enabled() (on bool, exe string) { return false, "" }
func Enable() (string, error)        { return "", errNotOffered }
func Disable() error                 { return nil }
