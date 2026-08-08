package library

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Store persists the scan between runs: the folders to watch, and the tracks
// found in them.
//
// It lives entirely in the user's own config/cache directories and never writes
// anything into the music folders themselves.
type Store struct{ Dir string }

// DefaultStore puts state under the user's config dir, falling back to the
// working directory when the OS will not name one (which is not fatal — a
// player that cannot remember its folders still plays).
func DefaultStore() *Store {
	dir, err := os.UserConfigDir()
	if err != nil {
		return &Store{Dir: ".madplayer"}
	}
	return &Store{Dir: filepath.Join(dir, "madplayer")}
}

// Config is the user's settings. Roots are the folders to scan.
type Config struct {
	Roots []string `json:"roots"`

	// Volume is 0..1, remembered across runs.
	Volume float64 `json:"volume"`
}

func (s *Store) configPath() string { return filepath.Join(s.Dir, "config.json") }
func (s *Store) tracksPath() string { return filepath.Join(s.Dir, "library.json") }

// LoadConfig reads the settings. A missing file is not an error — it is a first
// run, and the zero Config is the right answer for one.
func (s *Store) LoadConfig() (Config, error) {
	cfg := Config{Volume: 1}
	b, err := os.ReadFile(s.configPath())
	if errors.Is(err, fs.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return cfg, fmt.Errorf("config: %w", err)
	}
	if cfg.Volume <= 0 || cfg.Volume > 1 {
		cfg.Volume = 1
	}
	return cfg, nil
}

// SaveConfig writes the settings.
func (s *Store) SaveConfig(cfg Config) error {
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return s.write(s.configPath(), b)
}

// LoadTracks reads the cached scan. A missing or unreadable cache yields nil and
// no error: the answer is simply "rescan", never a failure the user must handle.
func (s *Store) LoadTracks() []*Track {
	b, err := os.ReadFile(s.tracksPath())
	if err != nil {
		return nil
	}
	var tracks []*Track
	if err := json.Unmarshal(b, &tracks); err != nil {
		return nil
	}
	return tracks
}

// SaveTracks writes the scan cache.
func (s *Store) SaveTracks(tracks []*Track) error {
	b, err := json.Marshal(tracks)
	if err != nil {
		return err
	}
	return s.write(s.tracksPath(), b)
}

// write replaces a file atomically, so an interrupted save cannot leave a
// half-written index that reads as a smaller library.
func (s *Store) write(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// TrackMap keys tracks by path, for the incremental rescan.
func TrackMap(tracks []*Track) map[string]*Track {
	m := make(map[string]*Track, len(tracks))
	for _, t := range tracks {
		m[t.Path] = t
	}
	return m
}
