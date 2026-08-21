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
// docs/architecture/embedding.md and docs/design.md.
package backend

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"daemonlord.ygg/madshare/app"
	"daemonlord.ygg/madshare/auth"
	"daemonlord.ygg/madshare/config"
	"daemonlord.ygg/madshare/database"
	"daemonlord.ygg/madshare/sources"

	"daemonlord.ygg/madplayer/internal/materialize"
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

	net     app.Network
	meshWhy string
}

// Options are the choices a person made that the backend cannot infer.
//
// Only the mesh is here, because only the mesh is optional. Everything else this
// package does — the library, the folders, the identity nobody types — is what
// madplayer IS, and a player with no library is not a configuration anybody
// wants.
type Options struct {
	// Mesh joins the madnetwork: this device becomes a node with its own key,
	// fetches from the swarm and seeds back what it fetched
	// (docs/design.md §"Level 2b"). Off by default — it costs bandwidth
	// and disk, and a player that only plays your own music should not quietly
	// start talking to strangers.
	Mesh bool
	// Peers are underlay peering URIs typed by hand. Usually empty: a device
	// normally reaches the mesh through what its home server publishes, or over
	// the local network. This is the fallback for somebody who has a peer and
	// neither of the other two.
	Peers []string
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
func Open(ctx context.Context, dataDir string, lg *log.Logger, opts Options) (*Backend, error) {
	if lg == nil {
		lg = log.Default()
	}
	if dataDir == "" {
		return nil, errors.New("backend: no data directory")
	}
	cfg, err := playerConfig(dataDir, opts).Prepare()
	if err != nil {
		return nil, err
	}

	fresh := !exists(cfg.Database.Path)
	if fresh {
		if cfg.Auth.InitialAdminPassword, err = app.GenerateSecret(); err != nil {
			return nil, err
		}
	}
	inst, err := app.Start(ctx, cfg, app.WithLogger(lg), app.WithMediaTools(tools{}))
	if err != nil && !fresh && errors.Is(err, auth.ErrNoAdminCredential) {
		// A database exists but holds no users: a first run that died between
		// creating the file and provisioning. Provision now.
		if cfg.Auth.InitialAdminPassword, err = app.GenerateSecret(); err != nil {
			return nil, err
		}
		inst, err = app.Start(ctx, cfg, app.WithLogger(lg), app.WithMediaTools(tools{}))
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
	if net, up := inst.Network(); up {
		// The listener node's standing rule, applied before anything can ask:
		// this device's own library is never advertised or served, whatever it
		// ends up able to place (docs/architecture/federation.md §"The
		// household"). Not fatal, but the mesh does not run without it — a device
		// that cannot be pinned must not run unpinned.
		if err := net.PublishNothing(ctx); err != nil {
			lg.Printf("madplayer: could not pin this device to publishing nothing: %v", err)
			b.meshWhy = "this device could not be pinned to publishing nothing, so the madnetwork is off"
		} else {
			b.net = net
		}
	}
	return b, nil
}

// Mesh is this device's madnetwork surface, and whether it is running.
func (b *Backend) Mesh() (app.Network, bool) { return b.net, b.net != nil }

// AddPeer dials an underlay peering URI now, rather than at the next start.
//
// It exists so that a peer typed into Settings is a thing that either connects
// or says why, in front of the person who typed it — the alternative is a
// restart before anybody learns whether the address was even the right shape.
// madshare's AddPeer is idempotent, so re-adding one already dialled is not a
// second link and needs no bookkeeping here.
//
// A mesh that is off is not an error to report as one: the setting is still
// worth saving, and the madnetwork section above it already says the mesh is
// off. Hence the bool rather than an error nobody should show.
func (b *Backend) AddPeer(uri string) (dialled bool, err error) {
	if b == nil || b.net == nil {
		return false, nil
	}
	if err := b.net.AddPeer(uri); err != nil {
		return false, err
	}
	return true, nil
}

// UnderlayPeer is one yggdrasil peering as it stands right now — the answer
// AddPeer cannot give.
//
// It is this package's own shape rather than madshare's, like every other type
// crossing this boundary, and it is narrower on purpose: the traffic counters
// and the remote key belong to a server's admin page, and a person looking at a
// phone is asking one question, which is whether this address is working.
type UnderlayPeer struct {
	URI string
	Up  bool
	// Inbound is a peering somebody dialled to US. A device behind a router has
	// none, so it is worth saying when there is one.
	Inbound bool
	Uptime  time.Duration
	Latency time.Duration
	// Problem is the last connection error and ProblemAge how long ago it was.
	// A down link that has never connected carries one; so does a link that is
	// up again, where it is history rather than news — hence the age.
	Problem    string
	ProblemAge time.Duration
}

// UnderlayPeers reports every peering this node holds: the ones typed into
// settings, the ones a home server published, the ones multicast found on the
// local network, and anything that dialled in. Down links come first.
//
// It is a snapshot for a screen, not a subscription, and it BLOCKS on the
// yggdrasil core's link actor — the same actor the dial loop runs on — so it
// belongs on a timer off the UI goroutine and never in a layout function.
func (b *Backend) UnderlayPeers() []UnderlayPeer {
	if b == nil || b.net == nil {
		return nil
	}
	live := b.net.UnderlayPeers()
	out := make([]UnderlayPeer, 0, len(live))
	for _, p := range live {
		out = append(out, UnderlayPeer{
			URI:        p.URI,
			Up:         p.Up,
			Inbound:    p.Inbound,
			Uptime:     time.Duration(p.UptimeSec) * time.Second,
			Latency:    time.Duration(p.LatencyMs * float64(time.Millisecond)),
			Problem:    p.LastError,
			ProblemAge: time.Duration(p.LastErrorAgeSec) * time.Second,
		})
	}
	return out
}

// MeshProblem says why the madnetwork is not running, or "" when it is (or was
// never asked for).
//
// It exists because a switch that silently does nothing is the worst possible
// way to tell somebody their mesh is off. Its original reason — no fpcalc on
// this host — is gone as of 2026-08-15, since the fingerprinting is this
// program's own now; what is left is the case that cannot be known before the
// node starts, a device that could not be pinned to publishing nothing.
func (b *Backend) MeshProblem() string { return b.meshWhy }

// playerConfig is the config a player runs on: one directory, no listener, and no
// allow-list on the folders its owner may add.
//
// It used to hand back a second value — why the mesh could not be honoured —
// which until 2026-08-15 was always the same answer: no fpcalc on this host.
// Nothing this function decides can refuse the mesh any more, so it says
// nothing. MeshProblem survives for the one reason that can still arise, and
// that one is only knowable after the node has started.
func playerConfig(dataDir string, opts Options) config.Config {
	cfg := config.Default()
	cfg.DataDir = dataDir
	// No [[listen]]: nothing is served, ever (docs/design.md).
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
	if !opts.Mesh {
		return cfg
	}
	// Fingerprinting is required of a federated node, on a player exactly as on a
	// server (decided 2026-08-09): a device that seeds is redistributing audio,
	// and without it there is no checking what was fetched against what it claims
	// to be. That requirement is unchanged and unweakened —
	// allow_missing_fingerprinting is still never set. What changed is that this
	// program satisfies it itself (internal/chroma, handed over as tools{}),
	// instead of needing somebody to install Chromaprint first.
	//
	// Until 2026-08-15 the switch was honoured only when fpcalc was on PATH, and
	// on Android it never could be, so the mesh could not come up there at all.
	cfg.Federation.Enabled = true
	cfg.Yggdrasil.Peers = opts.Peers
	// Local peer discovery, the opposite of a server's default. A phone finding
	// its home server over the wifi with no configuration at all is the case this
	// client exists in (docs/architecture/federation.md §"The household").
	cfg.Yggdrasil.Multicast = true
	// And nothing is shared back out: share_peers serves an HTTP endpoint, and
	// this program has no listener for one. Said explicitly rather than left to
	// the default, because the default is true and the reason it is harmless here
	// is a fact about this program rather than about the setting.
	no := false
	cfg.Yggdrasil.SharePeers = &no
	return cfg
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

	Tracks    int   // audio files the last scan found in the folder
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
			// Scanned, not Linked. Linked counts links the last scan CREATED, so
			// it is the whole folder the first time and zero on every rescan of an
			// unchanged one — which is how a folder holding thirteen tracks came
			// to describe itself as "0 tracks" in Settings. Scanned is what was
			// found: linked + skipped + failed.
			f.Tracks, f.Failed = s.Summary.Scanned, s.Summary.Failed
			if f.Tracks == 0 {
				// A summary written before madshare counted them at all.
				f.Tracks = s.Summary.Linked
			}
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

// --- the managed folder -----------------------------------------------------
//
// Network music kept on this device lands in a folder madplayer manages
// (docs/design.md §"Where the bytes live"). These three calls are all
// internal/materialize needs from the library, and the first two are
// deliberately the ORDINARY folder calls: a materialized file is an ordinary
// links-backed row in an ordinary data source, so there is exactly one kind of
// library entry. The third exists because that ordinary path reads the FILE, and
// a track pulled off the network is known by more than its bytes say.

// EnsureFolder makes a folder a data source, adding it when the library does not
// have it. The bool reports that it was added — and adding scans, so a caller
// that gets true has already had its files indexed.
//
// This is also how a thrown-away database recovers: the folder comes back as a
// source, the scan walks it, and music that was on disk the whole time is in the
// library again.
func (b *Backend) EnsureFolder(ctx context.Context, root string) (bool, error) {
	root = filepath.Clean(root)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return false, err
	}
	if _, ok, err := b.folderAt(ctx, root); err != nil {
		return false, err
	} else if ok {
		return false, nil
	}
	if _, err := b.AddFolder(ctx, root); err != nil {
		return false, err
	}
	return true, nil
}

// Register indexes what is new in a folder the library already has.
//
// It WAITS for any scan in flight first. The backend scans one folder at a time
// and answers a second request with an error rather than queueing it, so waiting
// is what turns "the user happens to be rescanning their library right now" from
// a failure into a delay.
func (b *Backend) Register(ctx context.Context, root string) error {
	id, ok, err := b.folderAt(ctx, filepath.Clean(root))
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%s is not in your library", root)
	}
	b.WaitScan()
	return b.RescanFolder(ctx, id)
}

// Describe tells the library what a kept track is, for the fields its own tags
// leave empty.
//
// The scan that just indexed the file could only read the file. What the track
// came WITH — the artist, album, album artist and numbers the catalogue showed
// when somebody pressed Keep — is not in the bytes for a great deal of music:
// metadata on a madshare is an overlay that is never written back into the file,
// so an album artist set in a web UI exists in no blob anywhere, and a WAV
// carries no tags this reader understands at all. Without this the Pathologic 2
// OST arrives as 47 tracks of "Unknown artist / Other" with titles taken from
// the filenames madplayer itself had just invented.
//
// It FILLS GAPS: madshare's FillMissingTags never overwrites a tag the file
// carries, so a well-tagged album keeps its own text and only the album-artist
// grouping (the field most often absent) is supplied.
//
// The wait is load-bearing. EnsureFolder and Register both start a scan and
// return; the row this patches does not exist until that scan reaches the file.
func (b *Backend) Describe(ctx context.Context, tr materialize.Track) error {
	if tr.Hash == "" {
		return nil // nothing to address the row by
	}
	b.WaitScan()

	patch := database.MetadataPatch{}
	set := func(field **string, value string) {
		if strings.TrimSpace(value) != "" {
			v := value
			*field = &v
		}
	}
	num := func(field **string, value int) {
		if value > 0 {
			v := strconv.Itoa(value)
			*field = &v
		}
	}
	set(&patch.Title, tr.Title)
	// The performer is the track's own credit; the album artist is what the
	// album is filed under. A soundtrack has both and they differ, which is the
	// whole reason the row needs telling.
	set(&patch.Artist, tr.Performer)
	set(&patch.AlbumArtist, tr.Artist)
	set(&patch.Album, tr.Album)
	num(&patch.TrackNumber, tr.Number)
	num(&patch.DiscNumber, tr.Disc)
	num(&patch.Year, tr.Year)
	if patch.IsEmpty() {
		return nil
	}
	return b.inst.FillMissingTags(ctx, tr.Hash, patch)
}

// folderAt finds the data source rooted at a path.
func (b *Backend) folderAt(ctx context.Context, root string) (id string, ok bool, err error) {
	folders, err := b.Folders(ctx)
	if err != nil {
		return "", false, err
	}
	for _, f := range folders {
		if filepath.Clean(f.Path) == root {
			return f.ID, true, nil
		}
	}
	return "", false, nil
}
