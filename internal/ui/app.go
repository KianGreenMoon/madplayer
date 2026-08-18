package ui

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"image"
	"image/color"

	"gioui.org/app"
	"gioui.org/io/system"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"daemonlord.ygg/madplayer/internal/about"
	"daemonlord.ygg/madplayer/internal/backend"
	"daemonlord.ygg/madplayer/internal/blobcache"
	"daemonlord.ygg/madplayer/internal/library"
	"daemonlord.ygg/madplayer/internal/madshare"
	"daemonlord.ygg/madplayer/internal/materialize"
	"daemonlord.ygg/madplayer/internal/mediasession"
	"daemonlord.ygg/madplayer/internal/mesh"
	"daemonlord.ygg/madplayer/internal/mpris"
	"daemonlord.ygg/madplayer/internal/player"
	"daemonlord.ygg/madplayer/internal/prefs"
	"daemonlord.ygg/madplayer/internal/queue"
	"daemonlord.ygg/madplayer/internal/remote"
)

type (
	C = layout.Context
	D = layout.Dimensions
)

// view is which panel is showing.
type view int

const (
	viewBrowse view = iota
	viewSearch
	viewSettings
	viewServers
	viewQueue
	viewCache
)

// level is the browse drill depth. Three levels, each addressed by a stable
// entity id and never by name.
type level int

const (
	levelArtists level = iota
	levelAlbums
	levelTracks
)

// App is the whole program's UI state.
//
// Browse rows are FETCHED ON NAVIGATION and held, never queried per frame: every
// list here is a database call into the embedded backend, and a layout function
// runs sixty times a second.
type App struct {
	win   *app.Window
	th    *material.Theme
	store *prefs.Store
	pl    *player.Player
	be    *backend.Backend
	lib   *library.Library
	cache *blobcache.Cache
	fetch *remote.Fetcher
	// art is the cover cache. It reads files on its own goroutines and asks for
	// a repaint when one lands.
	art *covers
	// keeper copies network music into the folder madplayer manages. Nil when
	// there is no downloader, which is an install whose cache would not open.
	keeper *materialize.Keeper
	// keepStrays is music somebody else put in the managed folder. Ignored, and
	// said out loud in Settings.
	keepStrays []string
	// keeping is a keep in flight. They run one at a time: each is a download,
	// and twenty at once compete for one link and finish no sooner.
	keeping bool
	// enrol keeps this device's standing with each home server when the mesh is
	// running: a vouch, a way onto the underlay, and an advertisement of what it
	// holds. Nil when the mesh is off, which is the default.
	enrol *mesh.Enrolment

	// mu guards everything a background load, scan or probe pass writes.
	mu      sync.Mutex
	cfg     prefs.Config
	folders []backend.Folder
	// homes is the signed-in servers in the mesh's vocabulary, kept whether or
	// not there is an enrolment yet to hand them to (see applyServers).
	homes []mesh.Server

	// the rows currently on screen, one slice per drill level
	artists []*library.Artist
	albums  []*library.Album
	tracks  []*library.Track
	found   library.SearchResults

	// albumArt maps an album row to the file its cover is read from. It is
	// rebuilt with the album list rather than kept: the keys are the rows
	// themselves, so a stale entry would pin an album nothing is showing.
	albumArt map[*library.Album]string

	// probs is the libraries that did not answer the last fetch. It is shown
	// beside the rows, never instead of them: a server being down must not blank
	// the music on this device.
	probs []library.Problem

	scanning bool
	loading  bool
	status   string
	// wantSettings is a background goroutine ASKING for the Settings panel,
	// applied by update on the UI goroutine. a.view itself is read unlocked all
	// over the layout code, so nothing but that goroutine may write it.
	wantSettings bool
	// notice is the one-line transient message above the player bar, and
	// noticeAt is when it was set — a message about something that happened is
	// only worth the line it costs for as long as it is news (see noticeLine).
	notice   string
	noticeAt time.Time
	srvBusy  bool
	srvMsg   string
	// peerMsg is the peer list's own status line: what the last add or remove
	// did. It belongs to that section rather than the player bar for the same
	// reason the cache page keeps its own — somebody who just typed an address
	// is looking at the box they typed it into.
	peerMsg string
	// underlay is what every yggdrasil peering is doing, re-read on a timer
	// while the madnetwork page is open (peers.go). It is held rather than asked
	// per frame because the read blocks on the core's link actor.
	underlay        []backend.UnderlayPeer
	underlayAt      time.Time
	underlayLoading bool
	cacheUsed       int64
	// seedCount and seedUsed are the OTHER cache: what this device seeds back to
	// the household, measured when the cache page is opened (see refreshSeedUsage).
	seedCount int
	seedUsed  int64
	// clearing is a clear in flight, and cacheMsg what the last one did. Both
	// belong to the cache page rather than to the player bar's notice line: a
	// person watching a number go down is looking at the page.
	clearing bool
	cacheMsg string
	// ceiling is the download limit in its three parts. The FIELD shows the
	// override — empty when there is none — while the hint names what the
	// default resolves to, so "empty" is never a value nobody can read.
	ceiling backend.Ceiling

	// title is the last string handed to the window manager, so retitle can tell
	// a changed track from sixty identical frames.
	title string

	// narrowUI is whether this frame is being laid out at phone width (under
	// narrowBar). Set at the top of layout each frame and only read from layout
	// code, so it needs no lock; it exists because the interesting decisions
	// (row buttons without hover, dropping the origin column) happen deep in
	// flexes where the local constraints no longer know the window's width.
	narrowUI bool

	// build is what this binary IS — commit, toolchain, embedded engine — read
	// once at start because it cannot change while the program runs. The About
	// section needs it, and so does the licence it satisfies (internal/about).
	build about.Build

	view  view
	level level

	artist *library.Artist
	album  *library.Album

	// widgets
	list widget.List
	// listPos remembers where each drill level was scrolled to, so coming back
	// up lands where you left rather than at the top (see setLevel).
	listPos [3]layout.Position
	// folderList is the Settings panel's own scroll state. It used to share the
	// queue panel's, which meant opening one scrolled the other.
	folderList                           widget.List
	queueList                            widget.List
	serverList                           widget.List
	search                               widget.Editor
	folderEd                             widget.Editor
	srvAddr, srvUser, srvPass            widget.Editor
	cacheEd                              widget.Editor
	seek                                 widget.Float
	vol                                  widget.Float
	seeking                              bool
	rows                                 []widget.Clickable
	crumbHome                            widget.Clickable
	crumbArt                             widget.Clickable
	btnSettings, btnServers, btnQueue    widget.Clickable
	btnPrev, btnPlay, btnNext            widget.Clickable
	btnShuffle, btnRepeat, btnClearQueue widget.Clickable
	btnAddFolder, btnRescan, btnUndo     widget.Clickable
	btnLocalOnly                         widget.Clickable
	btnCache                             widget.Clickable
	btnClearPlayed                       widget.Clickable
	btnClearSeeded, btnClearAll          widget.Clickable
	btnCopySource, btnCopyBuild          widget.Clickable
	btnLogCopy, btnLogSave, btnTestTone  widget.Clickable
	cacheList                            widget.List
	btnPlayAlbum, btnAlbumNext           widget.Clickable
	btnAlbumAdd, btnAlbumKeep            widget.Clickable
	btnKeepDirSave                       widget.Clickable
	keepDirEd                            widget.Editor
	keepTechnical                        widget.Bool
	btnSignIn, btnCacheSave              widget.Clickable
	meshOn                               widget.Bool
	// pairEd and pairing are the node-pairing experiment (pairing.go). The
	// editor is a top-level field so the typing-gate walk sees it; everything
	// else the experiment owns lives in the one struct, for easy removal.
	pairEd  widget.Editor
	pairing pairingState
	// peerEd and its buttons are the underlay peer list (peers.go): the third
	// way onto the mesh, for a device whose server publishes no peering and
	// whose network has none to discover.
	peerEd     widget.Editor
	btnAddPeer widget.Clickable
	rmPeer     []widget.Clickable
	// The clipboard buttons, one pair per settings text box — the only way to
	// paste into one on a phone (clipboard.go). The search box and the download
	// limit deliberately have none, and a test counts these against the editor
	// fields so a new box cannot quietly arrive without them.
	clipFolder, clipKeepDir      clipButtons
	clipCard, clipPeer           clipButtons
	clipAddr, clipUser, clipPass clipButtons
	// Settings is an index of pages rather than one scroll (settingsnav.go).
	// settingsPage is which one is open, settingsBtn one clickable per index
	// row, and btnSettingsBack the way out of a page.
	settingsPage    settingsPage
	settingsBtn     []widget.Clickable
	btnSettingsBack widget.Clickable
	// themeBtn is one clickable per entry of themes, in the same order.
	themeBtn []widget.Clickable
	rmFolder []widget.Clickable
	rmServer []widget.Clickable
	rmQueue  []widget.Clickable
	// rowNext and rowAdd are the per-row queue buttons, indexed exactly like
	// rows — see ensureRows, which grows all three together.
	rowNext []widget.Clickable
	rowAdd  []widget.Clickable
	// upQueue and downQueue reorder the queue panel.
	upQueue   []widget.Clickable
	downQueue []widget.Clickable
	// rowKeep is the per-row "keep on this device" button.
	rowKeep []widget.Clickable

	// saveQueue asks the one queue writer for a save. Buffered by one, so a
	// burst of edits collapses into a single write (see queuestate.go).
	saveQueue chan struct{}
	// done is closed when the window is. It is what stops the background writer,
	// so nothing touches the settings directory after the program is finished
	// with it.
	done chan struct{}

	// mediaBus is this player's presence on the desktop's media bus, or nil when
	// there is no session bus to be on. It is installed by Run — a program that
	// is not running has no business claiming a bus name — and read from the
	// player's goroutine, hence the atomic. Every method on the value is
	// nil-safe.
	mediaBus atomic.Pointer[mpris.Service]

	// mediaSession is the same presence on Android — the lock screen, the
	// quick-settings carousel and the foreground service that keeps playback
	// alive with the screen off. A stub everywhere else, and nil-safe for the
	// same reasons as mediaBus.
	mediaSession atomic.Pointer[mediasession.Service]
}

// New wires the UI to a player and the embedded backend.
func New(win *app.Window, pl *player.Player, be *backend.Backend) *App {
	return newApp(win, pl, be, prefs.Default())
}

// newApp is New with the settings directory named.
//
// The seam exists so a test can point the whole thing at a temporary directory
// BEFORE anything reads or writes it. Replacing the store afterwards is not the
// same thing: by then the queue has already been read, and the saver is already
// running against the real one — which is how a test run comes to overwrite the
// queue of whoever was listening to music at the time.
func newApp(win *app.Window, pl *player.Player, be *backend.Backend, store *prefs.Store) *App {
	a := &App{win: win, th: newTheme(), store: store, pl: pl, be: be, lib: library.New(be.Library())}
	a.build = about.Current()
	a.art = newCovers(win.Invalidate)
	// Embedded covers are written out here when the media bus asks for one: it
	// wants a URL, and art that lives inside an audio file has no path of its own.
	a.art.cache.SpillDir(filepath.Join(be.DataDir(), "covers"))
	a.list.Axis = layout.Vertical
	a.cacheList.Axis = layout.Vertical
	a.queueList.Axis = layout.Vertical
	a.folderList.Axis = layout.Vertical
	a.serverList.Axis = layout.Vertical
	a.search.SingleLine = true
	a.folderEd.SingleLine = true
	a.srvAddr.SingleLine = true
	a.srvUser.SingleLine = true
	a.srvPass.SingleLine = true
	a.srvPass.Mask = '•'
	a.cacheEd.SingleLine = true
	a.keepDirEd.SingleLine = true
	a.pairEd.SingleLine = true

	cfg, err := a.store.Load()
	if err != nil {
		a.status = "settings could not be read: " + err.Error()
	}
	a.cfg = cfg
	// The saved look, before the first frame: a light-theme user should not see
	// the window open dark and then correct itself.
	a.themeBtn = make([]widget.Clickable, len(themes))
	a.applyTheme(cfg.Theme)
	a.vol.Value = float32(cfg.Volume)
	pl.SetVolume(cfg.Volume)
	// The switch shows what was ASKED for, not what happened. They differ on a
	// device where the mesh could not come up, and the caption beside it is where
	// that is explained — a box that silently unticked itself would look like it
	// had not been saved.
	a.meshOn.Value = cfg.Mesh
	a.keepTechnical.Value = cfg.KeepTechnicalNames
	a.keepDirEd.SetText(cfg.KeepDir)

	// The ceiling is madshare's setting, not this client's: the same number a
	// server's settings card writes, read through the embedded backend. A read
	// that fails means no ceiling for this session rather than no downloads.
	ceiling, err := be.CacheCeiling(context.Background())
	if err != nil {
		a.status = "could not read the download limit: " + err.Error()
	}
	a.setCeiling(ceiling)

	// Remote audio lands beside the rest of what this install owns. A cache that
	// cannot be opened is not fatal: the device's own music plays regardless, and
	// only remote tracks are lost — so it is reported and the program carries on.
	a.cache, err = blobcache.Open(filepath.Join(be.DataDir(), "remote"), ceiling.Effective)
	if err != nil {
		a.status = "downloads are unavailable: " + err.Error()
	} else {
		a.fetch = remote.New(a.cache, log.Default())
		pl.SetFetcher(a.fetch)
	}
	// The mesh, when this device is a node.
	//
	// This MUST be built before applyServers below, and the ordering is the whole
	// bug it was written to fix: applyServers is the one place that tells every
	// consumer which servers there are, and it skips a nil enrolment silently. It
	// used to run first, so the enrolment loop started with an empty server list
	// and nothing ever filled it — no home server, so no capability token, so
	// Present returned false, so every swarm fetch declined without a word and
	// the madnetwork had never once been used. The comment here claimed this
	// order for a week while the code did the opposite.
	if node, up := be.Mesh(); up {
		a.enrol = mesh.New(node, log.Default())
		go a.enrol.Run(context.Background())
		if a.fetch != nil {
			// Downloads now prefer the swarm. The enrolment goes with it because a
			// mesh fetch presents a token, and only enrolment knows which one — the
			// two are installed together so there is no window in which the fetcher
			// would try the mesh with no vouch to present.
			a.fetch.SetSwarm(be, a.enrol)
		}
		// Whatever applyServers has already worked out, if it ran first.
		a.tellMesh()
	}

	// LAST of the wiring, on purpose: everything it hands the server list to has
	// to exist by now.
	a.applyServers()

	// Where network music is kept, and what is already in there. The reconcile
	// is what re-registers music still on disk after a library was thrown away,
	// so it runs on the way in rather than waiting to be asked.
	a.startKeeper()
	go a.reconcileKept()

	// The player advances the queue from its own goroutine, so a repaint has to
	// be asked for rather than assumed. The same signal is what warms the next
	// track and what keeps the media bus honest: it fires exactly when the queue
	// moves.
	pl.OnChange = func() {
		a.prefetchNext()
		a.mediaBus.Load().Update()
		a.mediaSession.Load().Update()
		a.markQueueDirty()
		win.Invalidate()
	}

	// The queue as it was left. Restored BEFORE the library loads, because it is
	// self-contained — every row carries the text and the path it needs — so the
	// player bar is populated the moment the window opens rather than after a
	// scan.
	a.saveQueue = make(chan struct{}, 1)
	a.done = make(chan struct{})
	a.restoreQueue()

	// Hand over the folders an older, self-scanning madplayer kept in its config,
	// so an upgrade re-imports the same music instead of looking like it lost it.
	legacy := a.store.TakeLegacyRoots(&a.cfg)
	go a.start(legacy)
	return a
}

// applyServers rebuilds the browse sources and the downloader from the saved
// server list. One place does it, so the two can never disagree about which
// servers exist.
func (a *App) applyServers() {
	a.mu.Lock()
	saved := append([]prefs.Server(nil), a.cfg.Servers...)
	a.mu.Unlock()

	servers := make([]library.Server, 0, len(saved))
	for _, s := range saved {
		servers = append(servers, library.Server{
			Base:   s.Base,
			Label:  serverLabel(s),
			Client: madshare.New(s.Base, s.Token),
		})
	}
	a.lib.SetServers(servers)
	if a.fetch != nil {
		a.fetch.SetServers(servers)
	}
	// The same list, in the mesh's own vocabulary. Signing out of a server has to
	// reach here too: it stops this device taking that server's word about
	// strangers, which is a fact about the mesh and not only about the screen.
	//
	// It is COMPUTED AND KEPT whether or not there is an enrolment to hand it to,
	// and that is the fix for the bug this whole path had: it used to skip a nil
	// enrolment silently, so running before the mesh was built meant the mesh
	// never learned there were any servers at all, and every swarm fetch declined
	// for want of a vouch. Keeping it makes the order stop mattering.
	homes := make([]mesh.Server, 0, len(saved))
	for _, s := range saved {
		homes = append(homes, mesh.Server{
			Base:   s.Base,
			Label:  serverLabel(s),
			Client: madshare.New(s.Base, s.Token),
		})
	}
	a.mu.Lock()
	a.homes = homes
	a.mu.Unlock()
	a.tellMesh()
}

// tellMesh hands the enrolment the servers this device is signed in to.
//
// Called from applyServers and again the moment an enrolment exists, so whichever
// of the two happens first, the mesh ends up knowing. A device that is not a node
// has nothing to tell.
func (a *App) tellMesh() {
	a.mu.Lock()
	enrol, homes := a.enrol, a.homes
	a.mu.Unlock()
	if enrol == nil {
		return
	}
	go enrol.SetServers(context.Background(), homes)
}

// serverLabel is what a server is called on screen: the name the person gave it,
// or its host, which is at least something they typed.
func serverLabel(s prefs.Server) string {
	if s.Label != "" {
		return s.Label
	}
	return strings.TrimPrefix(strings.TrimPrefix(s.Base, "https://"), "http://")
}

// setCeiling records the download limit and puts the override — not the
// effective value — in the editable field.
//
// That distinction is the whole point of the three states: showing the
// effective number in the box would turn "use the default" into a pinned
// override the moment anybody pressed Save.
func (a *App) setCeiling(c backend.Ceiling) {
	a.mu.Lock()
	a.ceiling = c
	a.mu.Unlock()
	if c.Override == nil {
		a.cacheEd.SetText("")
		return
	}
	a.cacheEd.SetText(fmt.Sprintf("%d", *c.Override>>20))
}

// prefetchNext warms the track after the current one, so the gap between two
// remote tracks is not a download.
func (a *App) prefetchNext() {
	if a.fetch == nil {
		return
	}
	items := a.pl.QueueItems()
	next := a.pl.QueueIndex() + 1
	if next >= 0 && next < len(items) {
		a.fetch.Prefetch(items[next])
	}
}

// start loads the library, importing any handed-over folders first.
func (a *App) start(adopt []string) {
	ctx := context.Background()
	for _, root := range adopt {
		if _, err := a.be.AddFolder(ctx, root); err != nil {
			a.setStatus("could not re-import " + root + ": " + err.Error())
			continue
		}
		a.setStatus("Importing " + root + "…")
		a.be.WaitScan()
	}
	a.loadFolders()
	a.reload()

	// Nothing to play from at all — no folders and no server — opens the panel
	// that fixes it. A person who plays only from a server has no folders on
	// purpose, and must not be sent to add one every launch.
	//
	// It ASKS rather than switching the view itself, and that is a correctness
	// fix rather than a style: a.view belongs to the UI goroutine, which reads
	// it unlocked in body, update, escape and the header, so writing it from
	// here — a background goroutine — was a data race with any frame that
	// happened to be laying out. It needed a first run with an empty library to
	// land, which is exactly the run this branch exists for (found 2026-08-18
	// while chasing its twin in the tests; see .issues/open-issues.md).
	a.mu.Lock()
	none := len(a.folders) == 0 && len(a.cfg.Servers) == 0
	a.mu.Unlock()
	if none {
		a.mu.Lock()
		a.wantSettings = true
		a.status = "Add a music folder to get started, or sign in to a server."
		a.mu.Unlock()
		a.win.Invalidate()
	}
}

// reload fetches the rows for whatever level is showing. Called after a scan, a
// folder change, or a drill.
func (a *App) reload() {
	a.mu.Lock()
	lvl, artist, album := a.level, a.artist, a.album
	a.loading = true
	a.mu.Unlock()

	ctx := context.Background()
	switch lvl {
	case levelAlbums:
		if artist != nil {
			albums, probs, err := a.lib.Albums(ctx, artist)
			a.finishLoad(func() { a.albums, a.loading = albums, false }, probs, err)
			a.loadAlbumArt(albums)
			return
		}
	case levelTracks:
		if album != nil {
			tracks, probs, err := a.lib.AlbumTracks(ctx, album)
			a.finishLoad(func() { a.tracks, a.loading = tracks, false }, probs, err)
			a.probeDurations()
			return
		}
	}
	artists, probs, err := a.lib.Artists(ctx)
	a.finishLoad(func() { a.artists, a.loading = artists, false }, probs, err)
}

// finishLoad applies a loaded result under the lock and reports what went wrong.
//
// Three outcomes, three different things said: a load that fails entirely must
// not look like an empty library, and a library that answered while another did
// not must show what it has with a note about the one that did not — never an
// error instead of the music.
func (a *App) finishLoad(apply func(), probs []library.Problem, err error) {
	a.mu.Lock()
	a.loading = false
	a.probs = probs
	if err != nil {
		a.status = "could not read the library: " + err.Error()
	} else {
		apply()
		a.status = ""
	}
	a.mu.Unlock()
	a.win.Invalidate()
}

// problemLine is the one-line summary of libraries that did not answer.
func problemLine(probs []library.Problem) string {
	switch len(probs) {
	case 0:
		return ""
	case 1:
		return probs[0].Label + " did not answer — showing everything else"
	}
	labels := make([]string, 0, len(probs))
	for _, p := range probs {
		labels = append(labels, p.Label)
	}
	return strings.Join(labels, ", ") + " did not answer — showing everything else"
}

// setLevel changes the drill depth and moves the one list widget with it.
//
// Going DOWN opens new content, which starts at the top. Coming back UP restores
// where that level was left — walking into an album from row 340 of an artist
// list and coming back to row 1 is the small thing that makes a large library
// tiring to browse.
func (a *App) setLevel(to level) {
	a.listPos[a.level] = a.list.Position
	if to > a.level {
		a.listPos[to] = layout.Position{}
	}
	a.level = to
	a.list.Position = a.listPos[to]
}

// drill opens an artist, loading their albums in the background.
func (a *App) drillArtist(ar *library.Artist) {
	a.mu.Lock()
	a.artist, a.album, a.albums = ar, nil, nil
	a.setLevel(levelAlbums)
	a.mu.Unlock()
	go a.reload()
}

// drillAlbum opens an album, loading its tracks in the background.
func (a *App) drillAlbum(al *library.Album) {
	a.mu.Lock()
	a.album, a.tracks = al, nil
	a.setLevel(levelTracks)
	a.mu.Unlock()
	go a.reload()
}

// loadFolders refreshes the folder list.
func (a *App) loadFolders() {
	folders, err := a.be.Folders(context.Background())
	a.mu.Lock()
	if err != nil {
		a.status = "could not read the folder list: " + err.Error()
	} else {
		a.folders = folders
		a.scanning = false
		for _, f := range folders {
			if f.Scanning() {
				a.scanning = true
			}
		}
	}
	a.mu.Unlock()
	a.win.Invalidate()
}

// watchScan follows a running scan to its end, refreshing the folder list as it
// goes and the library once when it finishes.
//
// It polls, deliberately: the scan runs inside the backend and reports through
// the data-source row rather than through a channel this process owns. One second
// is slow enough to cost nothing and fast enough that a small folder does not
// look stuck.
func (a *App) watchScan() {
	go func() {
		for {
			a.loadFolders()
			if !a.be.ScanRunning() {
				break
			}
			time.Sleep(time.Second)
		}
		a.loadFolders()
		a.reload()
	}()
}

// Rescan re-reads every folder, one at a time (the backend scans one folder at a
// time, and asking for two is an error rather than a queue).
func (a *App) Rescan() {
	a.mu.Lock()
	if a.scanning {
		a.mu.Unlock()
		return
	}
	a.scanning = true
	a.status = "Scanning…"
	folders := append([]backend.Folder(nil), a.folders...)
	a.mu.Unlock()
	a.win.Invalidate()

	go func() {
		ctx := context.Background()
		for _, f := range folders {
			if err := a.be.RescanFolder(ctx, f.ID); err != nil {
				a.setStatus(f.Path + ": " + err.Error())
				continue
			}
			a.be.WaitScan()
			a.loadFolders()
		}
		a.mu.Lock()
		a.scanning = false
		a.status = describeFolders(a.folders)
		a.mu.Unlock()
		a.reload()
	}()
}

// describeFolders is the status line: what was found, and what could not be read.
// Failures are named rather than only counted — a scan that indexed 9 of 10 files
// is a scan that lost one.
func describeFolders(folders []backend.Folder) string {
	tracks, failed, missing := 0, 0, 0
	for _, f := range folders {
		tracks += f.Tracks
		failed += f.Failed
		if f.Missing {
			missing++
		}
	}
	s := fmt.Sprintf("%s in %s", plural(tracks, "track"), plural(len(folders), "folder"))
	if failed > 0 {
		s += fmt.Sprintf(" · %d unreadable", failed)
	}
	if missing > 0 {
		s += " · " + plural(missing, "folder") + " not connected"
	}
	return s
}

// loadAlbumArt finds the file each album's cover is read from, in the
// background, after the rows are already on screen.
//
// This is the same rule the durations follow: the list renders IMMEDIATELY and
// the extras arrive later, because a list that waits for a cover per row is a
// list that does not appear. Only the device library is asked (see
// library.DeviceAlbumTracks) — a server's album shows the placeholder until
// something from it plays, at which point the download is on disk and the
// player bar reads the cover out of it.
func (a *App) loadAlbumArt(albums []*library.Album) {
	a.mu.Lock()
	a.albumArt = map[*library.Album]string{}
	a.mu.Unlock()
	if len(albums) == 0 {
		return
	}

	go func() {
		ctx := context.Background()
		found := 0
		for _, al := range albums {
			tracks, err := a.lib.DeviceAlbumTracks(ctx, al)
			if err != nil || len(tracks) == 0 {
				continue
			}
			path := albumCoverPath(tracks)
			if path == "" {
				continue
			}
			a.mu.Lock()
			// The album list can have been replaced while this ran; writing into
			// a map that is no longer the one on screen is harmless, but the rows
			// it describes are gone, so there is nothing to repaint for.
			if a.albumArt != nil {
				a.albumArt[al] = path
			}
			a.mu.Unlock()
			found++
			if found%8 == 0 {
				a.win.Invalidate()
			}
		}
		if found > 0 {
			a.win.Invalidate()
		}
	}()
}

// probeDurations fills in lengths the backend has none for.
//
// The backend measures duration with ffprobe; on a host without it every row
// would read "—" forever, and this client can decode the file itself. It is
// DISPLAY ONLY — nothing is written back — and it runs after the list is already
// on screen, which is the rule docs/ui/library-page.md sets.
func (a *App) probeDurations() {
	go func() {
		a.mu.Lock()
		todo := make([]*library.Track, 0, 32)
		for _, t := range a.tracks {
			// Only files this machine already holds: probing means decoding, and
			// decoding a remote track would download an album to fill in a
			// column of durations nobody asked for.
			if p := t.LocalPath(); t.Duration == 0 && p != "" && player.Decodable(p) {
				todo = append(todo, t)
			}
		}
		a.mu.Unlock()

		for i, t := range todo {
			d, err := player.Probe(t.LocalPath())
			if err != nil {
				continue
			}
			a.mu.Lock()
			t.Duration = d
			a.mu.Unlock()
			if i%25 == 0 {
				a.win.Invalidate()
			}
		}
		if len(todo) > 0 {
			a.win.Invalidate()
		}
	}()
}

// Run is the event loop.
//
// The two background services start here rather than in New, and for the same
// reason: a program that has not been started has no business claiming a bus
// name or writing to the settings directory. That also keeps a headless layout
// test — the only way this host can reach a panel — from touching either.
func (a *App) Run() error {
	// The desktop's media bus: the XF86Audio keys on a keyboard, the media widget
	// in GNOME's drop-down and KDE's tray, and playerctl. It is optional by
	// construction — a machine with no session bus is a normal machine, and a
	// music player that refused to start over one would be absurd — so a failure
	// is logged once and the program carries on with its own window.
	if svc, err := mpris.New("madplayer", controls{a.pl, a.art}, a.closeWindow); err != nil {
		log.Printf("madplayer: media keys and the desktop's media widget are unavailable: %v", err)
	} else {
		a.mediaBus.Store(svc)
		a.mediaBus.Load().Update()
	}
	// Android's media surfaces: the lock-screen controls and the foreground
	// service without which playback dies when the screen sleeps. A no-op stub
	// on every other platform; on Android a failure inside disables itself
	// with one log line, so there is no error to handle here.
	a.mediaSession.Store(mediasession.New(controls{a.pl, a.art}))
	a.mediaSession.Load().Update()
	go a.queueSaver()

	// Android paints the system bars itself, and its default is white — a
	// glaring strip over a dark player. Both bars take the bar color: the
	// status bar sits on the header, the navigation bar under the player bar,
	// and those two are the same color already. A no-op on the desktop.
	a.win.Option(app.StatusColor(colBar), app.NavigationColor(colBar))

	var ops op.Ops
	tick := time.NewTicker(a.pl.Tick())
	defer tick.Stop()
	go func() {
		for range tick.C {
			// The media bus learns the playhead here and nowhere else: it is the
			// one property a client polls rather than being told about.
			a.mediaBus.Load().Tick()
			a.mediaSession.Load().Tick()
			if a.pl.Playing() {
				a.win.Invalidate()
			}
		}
	}()

	for {
		switch e := a.win.Event().(type) {
		case app.DestroyEvent:
			// The writer is stopped BEFORE the last save, so the final state on
			// disk is this one and not whatever the heartbeat had in flight.
			close(a.done)
			a.save()
			a.writeQueue()
			_ = a.mediaBus.Load().Close()
			a.mediaSession.Load().Close()
			return e.Err
		case app.FrameEvent:
			gtx := app.NewContext(&ops, e)
			a.layout(gtx)
			e.Frame(gtx.Ops)
		}
	}
}

func (a *App) save() {
	a.mu.Lock()
	cfg := a.cfg
	a.mu.Unlock()
	cfg.Volume = a.pl.Volume()
	_ = a.store.Save(cfg)
}

// windowTitle is what the taskbar, the window list and an alt-tab switcher say
// this window is. A music player that is not the front window is exactly where
// the title has to answer "what is this" — so it names the track, and keeps the
// program's own name on the end so the entry is still identifiable when nothing
// is playing.
func (a *App) windowTitle() string {
	cur := a.pl.Current()
	if cur == nil {
		return "madplayer"
	}
	t := cur.Title
	if cur.Artist != "" {
		t += " — " + cur.Artist
	}
	return t + " · madplayer"
}

// closeWindow asks the window to close, from wherever.
//
// The Invalidate is NOT redundant, and finding that out cost an evening. Gio's
// X11 close sends the window a WM_DELETE_WINDOW ClientMessage with XSendEvent
// and never flushes Xlib's output buffer (os_x11.go, x11Window.close). On a
// window that is drawing — music playing, the 200ms tick invalidating — the next
// frame flushes it and the window closes. On an IDLE window nothing flushes, the
// message sits in the buffer, and the program simply refuses to quit.
//
// That is why the desktop's Quit appeared to work when it was first built and
// did not later: the difference was whether something happened to be playing.
// Asking for a frame is what posts the message.
func (a *App) closeWindow() {
	a.win.Perform(system.ActionClose)
	a.win.Invalidate()
}

// retitle pushes the title only when it CHANGED. Setting it every frame is sixty
// window-manager round trips a second for a string that moves once a song.
func (a *App) retitle() {
	if t := a.windowTitle(); t != a.title {
		a.title = t
		a.win.Option(app.Title(t))
	}
}

func (a *App) layout(gtx C) D {
	// One width decision per frame, taken where the constraints ARE the window.
	// Deeper layout code sits inside flexes whose remaining-space constraints
	// say nothing about the screen, so anything phone-conditional reads this
	// rather than measuring locally and guessing wrong.
	a.narrowUI = gtx.Constraints.Max.X < gtx.Dp(narrowBar)
	a.update(gtx)
	a.retitle()
	paint.FillShape(gtx.Ops, colBg, clip.Rect{Max: gtx.Constraints.Max}.Op())

	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(a.header),
		layout.Rigid(a.problemBanner),
		layout.Flexed(1, a.body),
		layout.Rigid(a.playerBar),
	)
}

// problemBanner names the libraries that did not answer, ABOVE the rows rather
// than instead of them. A server being unreachable is a footnote on a music
// collection, not a replacement for it.
func (a *App) problemBanner(gtx C) D {
	a.mu.Lock()
	line := problemLine(a.probs)
	a.mu.Unlock()
	if line == "" {
		return D{}
	}
	return layout.Inset{Top: 8, Bottom: 2, Left: 20, Right: 20}.Layout(gtx, func(gtx C) D {
		l := material.Caption(a.th, line)
		l.Color = colWarn
		l.MaxLines = 1
		return l.Layout(gtx)
	})
}

// update handles every control before anything is laid out, so a click and the
// frame it affects are the same frame.
func (a *App) update(gtx C) {
	// A view change asked for by a background goroutine is applied HERE, on the
	// goroutine that owns a.view and reads it unlocked everywhere else (see
	// start). One place to apply it, and it is a frame boundary.
	a.mu.Lock()
	if a.wantSettings {
		a.wantSettings = false
		a.view = viewSettings
	}
	a.mu.Unlock()

	if a.btnSettings.Clicked(gtx) {
		// Pressing Settings from inside one of its pages goes back to the index,
		// the way pressing an already-active tab returns it to its root
		// everywhere else. Only from the index does it close the panel — leaving
		// Settings altogether from three pages deep is a jump nobody asked for.
		if a.view == viewSettings && a.settingsPage != pageIndex {
			a.openSettingsPage(pageIndex)
		} else {
			a.view = toggleView(a.view, viewSettings)
		}
	}
	if a.btnCache.Clicked(gtx) {
		a.view = toggleView(a.view, viewCache)
		if a.view == viewCache {
			// Both numbers are directory walks, so they are measured when the
			// page that shows them is opened rather than every frame — and the
			// ceiling is re-read with them, so the page opens showing what is in
			// force rather than what this process last remembered.
			go func() {
				a.refreshCacheSize()
				a.refreshSeedUsage()
				a.reloadCeiling()
				a.win.Invalidate()
			}()
		}
	}
	if a.btnServers.Clicked(gtx) {
		a.view = toggleView(a.view, viewServers)
	}
	if a.btnQueue.Clicked(gtx) {
		a.view = toggleView(a.view, viewQueue)
	}
	if a.btnLocalOnly.Clicked(gtx) {
		a.toggleScope(gtx)
	}
	if a.btnClearQueue.Clicked(gtx) {
		a.pl.ClearQueue()
	}

	a.handleKeys(gtx)

	// Both walk the drill back, and both do it under the lock for the same
	// reason drillArtist does: a reload from the previous move can still be
	// reading the level and the row it is about.
	if a.crumbHome.Clicked(gtx) {
		a.mu.Lock()
		a.artist, a.album = nil, nil
		a.setLevel(levelArtists)
		a.mu.Unlock()
		go a.reload()
	}
	if a.crumbArt.Clicked(gtx) {
		a.mu.Lock()
		a.album = nil
		a.setLevel(levelAlbums)
		a.mu.Unlock()
		go a.reload()
	}

	// Search switches views without moving the control that did it, and
	// clearing returns to exactly the drill level you left.
	for {
		ev, ok := a.search.Update(gtx)
		if !ok {
			break
		}
		if _, isChange := ev.(widget.ChangeEvent); isChange {
			q := a.search.Text()
			if len([]rune(q)) >= 2 {
				a.view = viewSearch
				go a.doSearch(q)
			} else if a.view == viewSearch {
				a.view = viewBrowse
			}
		}
	}

	if a.btnPrev.Clicked(gtx) {
		a.pl.Prev()
	}
	if a.btnPlay.Clicked(gtx) {
		a.pl.Toggle()
	}
	if a.btnNext.Clicked(gtx) {
		a.pl.Next()
	}
	if a.btnShuffle.Clicked(gtx) {
		a.pl.ToggleShuffle()
	}
	if a.btnRepeat.Clicked(gtx) {
		a.pl.CycleRepeat()
	}
	if a.btnUndo.Clicked(gtx) {
		a.pl.Undo()
		a.notice = ""
	}

	if a.vol.Update(gtx) {
		a.pl.SetVolume(float64(a.vol.Value))
	}

	// A drag owns the seek bar until it is released; otherwise playback would
	// fight the user for the thumb.
	if a.seek.Update(gtx) {
		a.seeking = true
	} else if a.seeking && !a.seek.Dragging() {
		_, total := a.pl.Position()
		a.pl.Seek(float64(a.seek.Value) * total)
		a.seeking = false
	}

	if err := a.pl.TakeError(); err != nil {
		a.setNotice(err.Error())
	}
}

// doSearch runs a search in the background. The query is re-checked on arrival:
// a slower answer for text the user has already replaced is dropped rather than
// painted over the newer one.
func (a *App) doSearch(q string) {
	res, probs, err := a.lib.Search(context.Background(), q)
	a.mu.Lock()
	a.probs = probs
	if err != nil {
		a.status = "search failed: " + err.Error()
	} else if a.search.Text() == q {
		a.found = res
		a.status = ""
	}
	a.mu.Unlock()
	a.win.Invalidate()
}

// serversLabel is the header button: how many libraries are being browsed at
// once, which is the one thing a person needs to know before wondering where a
// row came from.
func (a *App) serversLabel() string {
	a.mu.Lock()
	n := len(a.cfg.Servers)
	a.mu.Unlock()
	if n == 0 {
		return "Servers"
	}
	return fmt.Sprintf("Servers (%d)", n)
}

func toggleView(cur, want view) view {
	if cur == want {
		return viewBrowse
	}
	return want
}

// toggleScope switches between everything this device can reach and only what it
// holds.
//
// It walks back to the artist list rather than staying where it was, and that is
// not tidiness: the drill can be standing inside a row the other scope does not
// have — an artist only the madnetwork knows, an album only a server holds — and
// narrowing underneath it would leave the breadcrumb naming something with no
// tracks under it. Going to the top is the one destination both scopes share.
func (a *App) toggleScope(gtx C) {
	to := library.ScopeDevice
	if a.lib.Scope() == library.ScopeDevice {
		to = library.ScopeAll
	}
	a.lib.SetScope(to)

	// Under the lock, like every other move through the drill: a reload started
	// by the PREVIOUS move may still be reading these (drillArtist set the
	// convention; the breadcrumb handlers had quietly skipped it).
	a.mu.Lock()
	a.artist, a.album, a.albums, a.tracks = nil, nil, nil, nil
	a.setLevel(levelArtists)
	a.mu.Unlock()

	if a.view == viewSearch {
		// A search's results belong to the scope that ran it, so it is re-run
		// rather than left showing rows from the other one.
		go a.doSearch(a.search.Text())
		return
	}
	a.view = viewBrowse
	go a.reload()
}

// scopeLabel names what the button will do, not what is showing. "Only local" is
// the narrowing on offer while everything is listed, and stays lit as the state
// once it has been taken.
func (a *App) scopeLabel() string { return "Only local" }

func (a *App) drillUp() bool {
	switch a.level {
	case levelTracks:
		a.album = nil
		a.setLevel(levelAlbums)
		go a.reload()
		return true
	case levelAlbums:
		a.artist = nil
		a.setLevel(levelArtists)
		go a.reload()
		return true
	}
	return false
}

func (a *App) header(gtx C) D {
	title := func(gtx C) D {
		l := material.H6(a.th, "madplayer")
		l.Color = colFg
		return l.Layout(gtx)
	}
	search := func(gtx C) D {
		ed := material.Editor(a.th, &a.search, "Search artists, albums, tracks…")
		ed.Color, ed.HintColor = colFg, colDim
		return filled(gtx, colSel, ed.Layout)
	}
	buttons := []layout.FlexChild{
		layout.Rigid(func(gtx C) D {
			return a.smallButton(gtx, &a.btnCache, "Cache", a.view == viewCache)
		}),
		layout.Rigid(layout.Spacer{Width: 8}.Layout),
		layout.Rigid(func(gtx C) D {
			return a.smallButton(gtx, &a.btnSettings, "Settings", a.view == viewSettings)
		}),
		layout.Rigid(layout.Spacer{Width: 8}.Layout),
		layout.Rigid(func(gtx C) D {
			return a.smallButton(gtx, &a.btnServers, a.serversLabel(), a.view == viewServers)
		}),
	}
	return bar(gtx, func(gtx C) D {
		return layout.Inset{Top: 10, Bottom: 10, Left: 16, Right: 16}.Layout(gtx, func(gtx C) D {
			// The one-row header needs the title, the search field and the
			// view buttons across; below narrowBar the buttons squeezed the
			// search out of existence and pushed Servers off the screen. So
			// the buttons take the first row, the search the second, and the
			// wordmark is dropped: a phone names its apps in the launcher and
			// the switcher, and the buttons alone fill the row.
			if gtx.Constraints.Max.X < gtx.Dp(narrowBar) {
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Rigid(func(gtx C) D {
						gtx.Constraints.Min.X = gtx.Constraints.Max.X
						return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle,
							Spacing: layout.SpaceBetween}.Layout(gtx, buttons...)
					}),
					layout.Rigid(layout.Spacer{Height: 8}.Layout),
					layout.Rigid(func(gtx C) D {
						gtx.Constraints.Min.X = gtx.Constraints.Max.X
						return search(gtx)
					}),
				)
			}
			row := append([]layout.FlexChild{
				layout.Rigid(title),
				layout.Rigid(layout.Spacer{Width: 16}.Layout),
				layout.Flexed(1, search),
				layout.Rigid(layout.Spacer{Width: 8}.Layout),
			}, buttons...)
			return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx, row...)
		})
	})
}

func (a *App) body(gtx C) D {
	switch a.view {
	case viewSettings:
		return a.settings(gtx)
	case viewServers:
		return a.serversPanel(gtx)
	case viewQueue:
		return a.queuePanel(gtx)
	case viewCache:
		return a.cachePanel(gtx)
	case viewSearch:
		return a.searchResults(gtx)
	default:
		return a.browse(gtx)
	}
}

// --- shared widgets ---------------------------------------------------------

func bar(gtx C, w layout.Widget) D {
	return layout.Stack{}.Layout(gtx,
		layout.Expanded(func(gtx C) D {
			paint.FillShape(gtx.Ops, colBar, clip.Rect{Max: gtx.Constraints.Min}.Op())
			return D{Size: gtx.Constraints.Min}
		}),
		layout.Stacked(func(gtx C) D {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return w(gtx)
		}),
	)
}

// filled paints a rounded background behind a widget and pads it.
func filled(gtx C, bg color.NRGBA, w layout.Widget) D {
	return layout.Stack{}.Layout(gtx,
		layout.Expanded(func(gtx C) D {
			// 8dp everywhere a corner rounds, the web UI's --radius.
			r := gtx.Dp(8)
			rect := clip.RRect{Rect: image.Rectangle{Max: gtx.Constraints.Min}, SE: r, SW: r, NE: r, NW: r}
			paint.FillShape(gtx.Ops, bg, rect.Op(gtx.Ops))
			return D{Size: gtx.Constraints.Min}
		}),
		layout.Stacked(func(gtx C) D {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return layout.UniformInset(8).Layout(gtx, w)
		}),
	)
}

func (a *App) smallButton(gtx C, click *widget.Clickable, label string, active bool) D {
	b := material.Button(a.th, click, label)
	b.Background = colSel
	b.Color = colFg
	if active {
		// Accent-filled buttons carry white text in every theme, like the web
		// UI's — colFg would vanish into a light theme's accent.
		b.Background, b.Color = colAccent, colOnAccent
	}
	b.CornerRadius = 8
	b.TextSize = unit.Sp(13)
	b.Inset = layout.Inset{Top: 6, Bottom: 6, Left: 12, Right: 12}
	return b.Layout(gtx)
}

// emptyState is the message shown when a panel has nothing to list. A load
// failure and an empty library must never look the same, which is why the
// caller passes the wording rather than this guessing it.
func (a *App) emptyState(gtx C, msg string) D {
	return layout.Inset{Top: 28, Left: 20, Right: 20}.Layout(gtx, func(gtx C) D {
		l := material.Body1(a.th, msg)
		l.Color = colDim
		return l.Layout(gtx)
	})
}

// itemsFromTracks converts library rows into queue items, capturing the display
// text so the queue survives the rows being refetched under it.
//
// It captures the CHOSEN copy, not the row: which library a queued track plays
// from is decided when it is queued, so re-browsing cannot silently move a
// queued track onto a different machine.
func (a *App) itemsFromTracks(tracks []*library.Track) []*queue.Item {
	out := make([]*queue.Item, len(tracks))
	for i, t := range tracks {
		it := &queue.Item{
			Title:    t.Title,
			Artist:   t.Artist,
			Album:    t.Album,
			Duration: t.Duration,
		}
		if c, ok := t.Best(); ok {
			it.Path, it.URL, it.Hash, it.Origin = c.Path, c.URL, c.Hash, c.Origin.Label
			// A madnetwork copy carries no address but its content, so the queue
			// carries what a fetch needs to turn that into audio: which server can
			// name the holders, and what container the bytes are in.
			if c.Network {
				it.Network, it.Base, it.Size, it.Codec = true, madnetworkBase(c.Origin), c.Size, c.Codec
			}
		}
		out[i] = it
	}
	return out
}

// networkHash is a copy's content hash when that is its ADDRESS — a madnetwork
// row, which has no path and no URL. A server track's hash is a cache key and
// must not become its row identity, or the same audio on two servers would be
// one row.
func networkHash(c library.Copy) string {
	if c.Network {
		return c.Hash
	}
	return ""
}

// madnetworkBase is the server behind a madnetwork origin: its source id with
// the marker taken off, which is the base URL a fetch asks for holders.
func madnetworkBase(o library.Origin) string {
	return strings.TrimSuffix(o.Source, library.MadnetworkMark)
}

// rowKey is how a browse row is matched against what is playing. It reads the
// copy that WOULD play, which is the same one itemsFromTracks captured.
func rowKey(t *library.Track) string {
	c, ok := t.Best()
	if !ok {
		return ""
	}
	return queue.KeyOf(c.Path, c.URL, networkHash(c))
}

// setNotice writes the one-line notice and starts its clock. Every write goes
// through here so that none can be the one that never expires.
func (a *App) setNotice(msg string) {
	a.notice, a.noticeAt = msg, time.Now()
}

// setNoticeAsync writes the one-line notice from a background goroutine.
//
// The notice is otherwise set during update(), on the UI's own goroutine, so the
// lock and the repaint are only needed on this path — which is every long
// operation that has something to say while it runs.
func (a *App) setNoticeAsync(msg string) {
	a.mu.Lock()
	a.setNotice(msg)
	a.mu.Unlock()
	a.win.Invalidate()
}

func (a *App) setStatus(msg string) {
	a.mu.Lock()
	a.status = msg
	a.mu.Unlock()
	a.win.Invalidate()
}
