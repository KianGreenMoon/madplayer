// Package prefs stores the few settings that belong to this device.
//
// They are preferences, not an account: there is no password here and nothing to
// sign in to (docs/ui/madplayer.md §"There is no local account"). The library
// itself lives in the embedded backend's data directory — this file is only what
// the UI would otherwise forget between runs.
package prefs

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Store is the settings file's home.
type Store struct{ Dir string }

// Default puts settings under the user's config dir, falling back to the working
// directory when the OS will not name one — not fatal, since a player that
// forgets its volume still plays.
func Default() *Store {
	dir, err := os.UserConfigDir()
	if err != nil {
		return &Store{Dir: ".madplayer"}
	}
	return &Store{Dir: filepath.Join(dir, "madplayer")}
}

// Config is what survives a restart.
type Config struct {
	// Volume is 0..1.
	Volume float64 `json:"volume"`

	// Servers are the remote madshares this device is signed in to. Each carries
	// an API token, which is why this file is written 0600 (see Save).
	Servers []Server `json:"servers,omitempty"`

	// The download cache's ceiling is deliberately NOT here. It is madshare's own
	// runtime setting, read and written through the embedded backend
	// (backend.CacheLimit), so the number is the same one a server's settings card
	// writes rather than a second copy that can disagree with it.

	// Roots is no longer used: music folders are data sources in the backend now.
	// It is still read so an install that predates the embedded backend can hand
	// its folders over once (see TakeLegacyRoots) instead of appearing to have
	// lost them.
	Roots []string `json:"roots,omitempty"`
}

// Server is one remote madshare this device is signed in to.
//
// The base URL is the IDENTITY: adding the same address twice updates the entry
// rather than making a second one, because two rows for one server would sign in
// twice and merge that server's library with itself.
type Server struct {
	Base  string `json:"base"`
	Label string `json:"label"`
	// Username is display only — who this device is signed in as. The token is
	// what actually authenticates.
	Username string `json:"username"`
	// Token is an API token minted by that server (madshare.SignIn). It is stored
	// in plain text: there is no keyring here, and the file is 0600. Revoking it
	// is done on the server, where it is listed by name.
	Token string `json:"token"`
}

// SetServer adds a server or updates the one already at that address.
func (c *Config) SetServer(s Server) {
	for i := range c.Servers {
		if c.Servers[i].Base == s.Base {
			c.Servers[i] = s
			return
		}
	}
	c.Servers = append(c.Servers, s)
}

// RemoveServer forgets a server. The token stays valid on the server until it is
// revoked there — forgetting it here is what signing out of THIS device means.
func (c *Config) RemoveServer(base string) {
	out := c.Servers[:0]
	for _, s := range c.Servers {
		if s.Base != base {
			out = append(out, s)
		}
	}
	c.Servers = out
}

func (s *Store) path() string        { return filepath.Join(s.Dir, "config.json") }
func (s *Store) legacyIndex() string { return filepath.Join(s.Dir, "library.json") }

// Load reads the settings. A missing file is a first run, not an error.
func (s *Store) Load() (Config, error) {
	cfg := Config{Volume: 1}
	b, err := os.ReadFile(s.path())
	if errors.Is(err, fs.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return cfg, fmt.Errorf("settings: %w", err)
	}
	if cfg.Volume <= 0 || cfg.Volume > 1 {
		cfg.Volume = 1
	}
	return cfg, nil
}

// Save writes the settings, replacing the file atomically so an interrupted write
// cannot leave half a config behind.
//
// The mode is 0600, not 0644: this file holds API tokens for the servers this
// device is signed in to, and each one is that account's full access to its
// server. The temporary file is written with the same mode, so the credential is
// never briefly world-readable in the gap before the rename.
func (s *Store) Save(cfg Config) error {
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return err
	}
	tmp := s.path() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	// WriteFile leaves an existing file's mode alone, so an install that saved a
	// 0644 config before tokens existed keeps it. Say it outright.
	if err := os.Chmod(tmp, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path())
}

// TakeLegacyRoots returns the folders an older madplayer scanned itself, and
// forgets them — including the index it kept beside them, which nothing reads any
// more.
//
// It hands over rather than copies: the caller imports each folder as a data
// source, and the next call returns nothing. Losing the list silently would look
// exactly like the library having been thrown away.
func (s *Store) TakeLegacyRoots(cfg *Config) []string {
	roots := cfg.Roots
	if len(roots) == 0 {
		return nil
	}
	cfg.Roots = nil
	_ = s.Save(*cfg)
	// The old scan cache is this program's own file and is now dead weight.
	_ = os.Remove(s.legacyIndex())
	return roots
}
