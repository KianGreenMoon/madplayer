//go:build windows

package autostart

import (
	"errors"
	"fmt"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// Windows: a value under the per-user Run key, which Explorer runs at login.
// The registry over a Startup-folder shortcut because a value is one string
// that can be read back exactly — a .lnk is a binary format this program
// would otherwise have to write and parse itself — and because the Run key
// needs no elevation and no shell API.

// runKey is the per-user login-run key.
const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`

// Offered reports that a login entry can be written here.
func Offered() bool { return true }

// Path names the entry, for messages: the registry value's full address.
func Path() (string, error) {
	return `HKEY_CURRENT_USER\` + runKey + `\` + Name, nil
}

// Enabled reports whether our value exists, and which program it starts.
func Enabled() (on bool, exe string) {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		return false, ""
	}
	defer k.Close()
	cmd, _, err := k.GetStringValue(Name)
	if err != nil {
		return false, ""
	}
	return true, exeOf(cmd)
}

// Enable writes the value for the running program.
func Enable() (string, error) {
	exe, err := executable()
	if err != nil {
		return "", err
	}
	if strings.ContainsAny(exe, "\"") {
		return "", fmt.Errorf("the program's path %q cannot be written into a Run entry", exe)
	}
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return "", err
	}
	defer k.Close()
	if err := k.SetStringValue(Name, command(exe)); err != nil {
		return "", err
	}
	p, _ := Path()
	return p, nil
}

// Disable removes the value. A value that is already gone is fine.
func Disable() error {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			return nil
		}
		return err
	}
	defer k.Close()
	if err := k.DeleteValue(Name); err != nil && !errors.Is(err, registry.ErrNotExist) {
		return err
	}
	return nil
}
