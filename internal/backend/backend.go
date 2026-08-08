// Package backend runs madshare in this process.
//
// It is the only package that talks to the embedded server, and it is
// deliberately small: it owns the data directory, the identity nobody types, and
// the folders-as-data-sources vocabulary. Everything else — browsing, playback,
// the queue — goes through what it exposes.
//
// There is no listener and no port. madplayer holds one person's music on their
// own machine, so being reachable would add an attack surface in exchange for
// nothing it needs; the facade's Serve half is simply never called. See
// docs/architecture/embedding.md and docs/ui/madplayer.md.
package backend

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"daemonlord.ygg/madshare/app"
	"daemonlord.ygg/madshare/auth"
	"daemonlord.ygg/madshare/config"
	"daemonlord.ygg/madshare/sources"
)

// ownerName is the single identity a madplayer install has. It exists because
// madshare refuses to start on an empty users table, and because data sources,
// uploads and playlists are attributed to somebody. Nobody types it and nobody
// sees it — see Open for what happens to its password.
const ownerName = "owner"

// Backend is the embedded madshare node.
type Backend struct {
	inst  *app.Instance
	owner sql.NullInt64
	dir   string
	log   *log.Logger
}

// Open starts madshare against dataDir, provisioning the owner on first run.
//
// The generated password is used once and thrown away: nothing on this machine
// can log in, because nothing serves, so a credential that exists nowhere is
// strictly better than one lying in a file. On a data dir that already has a
// user, no credential is passed at all — otherwise madshare would warn on every
// single launch about an unused one, advising the reader to unset an environment
// variable this program never sets. The interrupted-first-run case (a database
// with no users) is covered by retrying with a fresh secret rather than by
// guessing up front.
func Open(ctx context.Context, dataDir string, lg *log.Logger) (*Backend, error) {
	if lg == nil {
		lg = log.Default()
	}
	if dataDir == "" {
		return nil, errors.New("backend: no data directory")
	}
	cfg, err := playerConfig(dataDir)
	if err != nil {
		return nil, err
	}

	fresh := !exists(cfg.Database.Path)
	if fresh {
		if cfg.Auth.InitialAdminPassword, err = app.GenerateSecret(); err != nil {
			return nil, err
		}
	}
	inst, err := app.Start(ctx, cfg, app.WithLogger(lg))
	if err != nil && !fresh && errors.Is(err, auth.ErrNoAdminCredential) {
		// A database exists but holds no users: a first run that died between
		// creating the file and provisioning. Provision now.
		if cfg.Auth.InitialAdminPassword, err = app.GenerateSecret(); err != nil {
			return nil, err
		}
		inst, err = app.Start(ctx, cfg, app.WithLogger(lg))
	}
	if err != nil {
		return nil, err
	}

	id, ok, err := inst.UserID(ctx, ownerName)
	if err != nil {
		inst.Stop(context.Background())
		return nil, fmt.Errorf("backend: resolve owner: %w", err)
	}
	b := &Backend{inst: inst, dir: dataDir, log: lg}
	if ok {
		// Not fatal when absent: an install provisioned under a different name
		// still browses and plays, it just cannot attribute what it imports.
		b.owner = sql.NullInt64{Int64: id, Valid: true}
	}
	return b, nil
}

// playerConfig is the config a player runs on: one directory, no listener, and no
// allow-list on the folders its owner may add.
func playerConfig(dataDir string) (config.Config, error) {
	cfg := config.Default()
	cfg.DataDir = dataDir
	// No [[listen]]: nothing is served, ever (docs/ui/madplayer.md).
	cfg.Listen = nil
	// The person clicking "Add folder" is at the keyboard on their own machine,
	// and there is no reachable surface for an allow-list to protect
	// (docs/architecture/embedding.md §"A boundary a listener-less deployment may
	// drop").
	cfg.Sources.AllowAny = true
	cfg.Auth.InitialAdminUser = ownerName
	// This client's own default for the download cache, in the layer a server
	// would fill from its TOML file — so the settings panel's "Default" is a real
	// number here rather than "no limit".
	cfg.Federation.CacheMaxMB = DefaultCacheMB
	return cfg.Prepare()
}

// Close shuts the node down. Safe to call more than once.
func (b *Backend) Close() {
	if b != nil && b.inst != nil {
		b.inst.Stop(context.Background())
	}
}

// Library is the browse and playback surface.
func (b *Backend) Library() app.Library { return b.inst.Library() }

// Ceiling is the limit on downloaded audio in the three parts a settings screen
// needs: what is in force, the override if one is set, and what this client's
// default is.
//
// Restated here rather than handing out madshare's own struct, because the rule
// that keeps the embedded half out of the widgets is that this package is the
// ONLY one importing madshare — and the toolkit has an `app` package of its own,
// so the two must never meet in one file.
type Ceiling struct {
	// Effective is the limit actually applied, in bytes. 0 = no limit.
	Effective int64
	// Override is the chosen value, or nil when the default applies. A non-nil 0
	// is a real choice meaning "no limit", which is why it is a pointer.
	Override *int64
	// Default is what clearing the override lands on — DefaultCacheMB, in bytes.
	Default int64
}

// CacheCeiling reports that limit.
//
// It is madshare's own setting, read through the facade rather than kept in this
// client's config file: the same policy a server's settings card writes, so
// there is no second copy to disagree with it
// (docs/architecture/madnetwork-cache.md §"The retention ceiling").
func (b *Backend) CacheCeiling(ctx context.Context) (Ceiling, error) {
	c, err := b.inst.CacheCeiling(ctx)
	if err != nil {
		return Ceiling{}, err
	}
	return Ceiling{Effective: c.Effective, Override: c.Override, Default: c.Default}, nil
}

// SetCacheCeiling writes the override: nil clears it back to this client's
// default, a value pins it, and 0 pins "no limit". The caller applies the
// resulting number to its own cache; this records it.
func (b *Backend) SetCacheCeiling(ctx context.Context, maxBytes *int64) error {
	return b.inst.SetCacheCeiling(ctx, maxBytes)
}

// DefaultCacheMB is this client's ceiling on downloaded audio, in MiB.
//
// It is supplied as CONFIG, in the layer a server fills from its TOML file
// (docs/architecture/madnetwork-cache.md §"The retention ceiling"), which is
// what makes it a real default rather than a value written into the settings
// once: the person can override it and can clear the override again, and
// clearing lands back here rather than on "no limit".
//
// A server ships 0 (no limit) because a guessed ceiling would start deleting
// other people's content on a node that already has some. A player has no such
// history — its cache starts empty — and a phone with no ceiling at all is a
// worse default than a stated one. 2 GiB comes from the shape of the content: a
// FLAC album is roughly 300 MB.
const DefaultCacheMB = 2048

// DataDir is where everything this install owns lives.
func (b *Backend) DataDir() string { return b.dir }

// Folder is one music folder, scanned in place.
type Folder struct {
	ID     string
	Name   string
	Path   string
	Status string // "active" | "scanning" | "error"

	Tracks    int   // files linked by the last scan
	Failed    int   // files the last scan could not read
	ScannedAt int64 // 0 = never finished a scan

	// Missing reports that the folder is not there right now — an unplugged
	// drive, an ejected card, a folder somebody moved. On a server that is an
	// incident; here it is Tuesday, so it is a state to display and not an error
	// to raise.
	Missing bool
}

// Scanning reports whether the folder is being read right now.
func (f Folder) Scanning() bool { return f.Status == "scanning" }

// Folders lists the music folders, newest first.
func (b *Backend) Folders(ctx context.Context) ([]Folder, error) {
	list, err := b.inst.Sources().List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Folder, 0, len(list))
	for _, s := range list {
		f := Folder{ID: s.ID, Name: s.Name, Path: s.Root, Status: s.Status, Missing: !isDir(s.Root)}
		if s.ScannedAt != nil {
			f.ScannedAt = *s.ScannedAt
		}
		if s.Summary != nil {
			f.Tracks, f.Failed = s.Summary.Linked, s.Summary.Failed
		}
		out = append(out, f)
	}
	return out, nil
}

// AddFolder scans a folder in place and returns it.
//
// Nothing is copied, moved or written into the folder — the import is one symlink
// per file, pointing at the original, which is the same hard invariant the
// server's data sources carry. The scan runs in the background; the returned
// Folder is already "scanning".
func (b *Backend) AddFolder(ctx context.Context, path string) (Folder, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return Folder{}, errors.New("type a folder path")
	}
	abs, err := filepath.Abs(expandHome(path))
	if err != nil {
		return Folder{}, err
	}
	if !isDir(abs) {
		// Said plainly and early: a silently-ignored typo looks exactly like an
		// empty library.
		return Folder{}, fmt.Errorf("%s is not a folder", abs)
	}
	src, err := b.inst.Sources().Add(ctx, filepath.Base(abs), abs, b.owner)
	if err != nil {
		return Folder{}, folderError(err)
	}
	return Folder{ID: src.ID, Name: src.Name, Path: src.Root, Status: src.Status}, nil
}

// RescanFolder re-reads a folder: new files are added, unchanged ones (same size
// and mtime) are skipped.
func (b *Backend) RescanFolder(ctx context.Context, id string) error {
	if _, err := b.inst.Sources().Rescan(ctx, id, b.owner); err != nil {
		return folderError(err)
	}
	return nil
}

// RemoveFolder forgets a folder and the tracks only it referenced. The originals
// are untouched — removing a folder from the library never deletes music.
func (b *Backend) RemoveFolder(ctx context.Context, id string) error {
	if _, err := b.inst.Sources().Remove(ctx, id); err != nil {
		return folderError(err)
	}
	return nil
}

// ScanRunning reports whether a scan is in progress. One runs at a time.
func (b *Backend) ScanRunning() bool { return b.inst.Sources().Running() }

// WaitScan blocks until the running scan finishes. Callers that add or rescan
// several folders need it, because the backend scans one at a time and answers a
// second request with an error rather than queueing it.
func (b *Backend) WaitScan() { b.inst.Sources().Wait() }

// folderError turns the manager's sentinels into something worth showing a
// person. ErrRootNotAllowed cannot happen here (the allow-list is off) and is
// mapped anyway, because a silent empty string would be worse if it ever did.
func folderError(err error) error {
	switch {
	case errors.Is(err, sources.ErrBusy):
		return errors.New("another folder is being scanned — try again when it finishes")
	case errors.Is(err, sources.ErrInvalidRoot):
		return errors.New("that path is not a folder this program can read")
	case errors.Is(err, sources.ErrRootNotAllowed):
		return errors.New("that folder is outside the allowed paths")
	case errors.Is(err, sources.ErrDisabled):
		return errors.New("importing folders is disabled in this build")
	}
	return err
}

// expandHome resolves a leading ~ so a typed path behaves the way a shell would.
func expandHome(p string) string {
	if p != "~" && !strings.HasPrefix(p, "~"+string(filepath.Separator)) {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	return filepath.Join(home, strings.TrimPrefix(p, "~"))
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
