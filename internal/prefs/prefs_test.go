package prefs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveKeepsTheCredentialFilePrivate(t *testing.T) {
	s := &Store{Dir: t.TempDir()}

	// An install that saved a config before tokens existed left a world-readable
	// file behind; saving a token into it must not inherit that mode.
	if err := os.WriteFile(filepath.Join(s.Dir, "config.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := Config{Volume: 0.5}
	cfg.SetServer(Server{Base: "http://host:3000", Label: "host", Username: "kian", Token: "tok_secret"})
	if err := s.Save(cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}

	info, err := os.Stat(filepath.Join(s.Dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("config mode = %04o, want 0600 — it holds API tokens", mode)
	}

	got, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got.Servers) != 1 || got.Servers[0].Token != "tok_secret" {
		t.Fatalf("servers round-tripped as %+v", got.Servers)
	}
}

func TestSetServerIsKeyedByAddress(t *testing.T) {
	var cfg Config
	cfg.SetServer(Server{Base: "http://host:3000", Username: "kian", Token: "one"})
	cfg.SetServer(Server{Base: "http://other:3000", Username: "kian", Token: "two"})
	// Signing in again to a server already known replaces the credential rather
	// than adding a second row — two rows for one server would merge that
	// server's library with itself.
	cfg.SetServer(Server{Base: "http://host:3000", Username: "kian", Token: "three"})

	if len(cfg.Servers) != 2 {
		t.Fatalf("servers = %d, want 2: %+v", len(cfg.Servers), cfg.Servers)
	}
	if cfg.Servers[0].Token != "three" {
		t.Errorf("token = %q, want the re-signed-in one", cfg.Servers[0].Token)
	}

	cfg.RemoveServer("http://host:3000")
	if len(cfg.Servers) != 1 || cfg.Servers[0].Base != "http://other:3000" {
		t.Errorf("after remove: %+v", cfg.Servers)
	}
}

// The download cache's ceiling deliberately does NOT live here — it is
// madshare's own runtime setting, reached through the embedded backend, so the
// number is the same one a server's settings card writes. Nothing in this
// package reads or writes one.

// The madnetwork is on by default, and turning it OFF has to stick.
//
// This is the volume bug in a second place, and it would have been worse: with
// `omitempty` on a bool, writing false writes nothing, an absent key reads as
// the default, and a player somebody deliberately took off the network would
// quietly rejoin it on the next launch.
func TestTurningTheMadnetworkOffSticks(t *testing.T) {
	dir := t.TempDir()
	s := &Store{Dir: dir}

	// A first run has no file at all: the default applies.
	first, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !first.Mesh {
		t.Error("the madnetwork was off on a first run, want on by default")
	}

	// Turned off explicitly.
	off := first
	off.Mesh = false
	if err := s.Save(off); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(filepath.Join(dir, "config.json")); err != nil {
		t.Fatal(err)
	} else if !strings.Contains(string(raw), "\"mesh\": false") {
		t.Fatalf("config.json does not record the decision:\n%s", raw)
	}

	back, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if back.Mesh {
		t.Error("the madnetwork switched itself back on — an explicit false was read as absent")
	}

	// And back on again, which must also survive.
	on := back
	on.Mesh = true
	if err := s.Save(on); err != nil {
		t.Fatal(err)
	}
	if again, err := s.Load(); err != nil || !again.Mesh {
		t.Errorf("mesh = %v err = %v after turning it on", again.Mesh, err)
	}
}

// An install that predates the default has no key, and takes the new default —
// which is the point of changing it.
func TestAnOlderConfigWithNoMeshKeyTakesTheDefault(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"volume":0.7}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := (&Store{Dir: dir}).Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Mesh {
		t.Error("a config written before the default changed did not pick it up")
	}
	if cfg.Volume != 0.7 {
		t.Errorf("volume = %v, want the saved 0.7", cfg.Volume)
	}
}
