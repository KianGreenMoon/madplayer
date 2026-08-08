package prefs

import (
	"os"
	"path/filepath"
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
