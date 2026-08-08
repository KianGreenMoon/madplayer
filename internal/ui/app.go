package ui

import (
	"context"
	"fmt"
	"sync"
	"time"

	"image"
	"image/color"

	"gioui.org/app"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"daemonlord.ygg/madplayer/internal/backend"
	"daemonlord.ygg/madplayer/internal/library"
	"daemonlord.ygg/madplayer/internal/player"
	"daemonlord.ygg/madplayer/internal/prefs"
	"daemonlord.ygg/madplayer/internal/queue"
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
	viewQueue
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

	// mu guards everything a background load, scan or probe pass writes.
	mu      sync.Mutex
	cfg     prefs.Config
	folders []backend.Folder

	// the rows currently on screen, one slice per drill level
	artists []*library.Artist
	albums  []*library.Album
	tracks  []*library.Track
	found   library.SearchResults

	scanning bool
	loading  bool
	status   string
	notice   string

	view  view
	level level

	artist *library.Artist
	album  *library.Album

	// widgets
	list                                 widget.List
	queueList                            widget.List
	search                               widget.Editor
	folderEd                             widget.Editor
	seek                                 widget.Float
	vol                                  widget.Float
	seeking                              bool
	rows                                 []widget.Clickable
	crumbHome                            widget.Clickable
	crumbArt                             widget.Clickable
	btnSettings, btnQueue                widget.Clickable
	btnPrev, btnPlay, btnNext            widget.Clickable
	btnShuffle, btnRepeat, btnClearQueue widget.Clickable
	btnAddFolder, btnRescan, btnUndo     widget.Clickable
	rmFolder                             []widget.Clickable
	rmQueue                              []widget.Clickable
}

// New wires the UI to a player and the embedded backend.
func New(win *app.Window, pl *player.Player, be *backend.Backend) *App {
	a := &App{win: win, th: newTheme(), store: prefs.Default(), pl: pl, be: be, lib: library.New(be.Library())}
	a.list.Axis = layout.Vertical
	a.queueList.Axis = layout.Vertical
	a.search.SingleLine = true
	a.folderEd.SingleLine = true

	cfg, err := a.store.Load()
	if err != nil {
		a.status = "settings could not be read: " + err.Error()
	}
	a.cfg = cfg
	a.vol.Value = float32(cfg.Volume)
	pl.SetVolume(cfg.Volume)

	// The player advances the queue from its own goroutine, so a repaint has to
	// be asked for rather than assumed.
	pl.OnChange = func() { win.Invalidate() }

	// Hand over the folders an older, self-scanning madplayer kept in its config,
	// so an upgrade re-imports the same music instead of looking like it lost it.
	legacy := a.store.TakeLegacyRoots(&a.cfg)
	go a.start(legacy)
	return a
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

	a.mu.Lock()
	none := len(a.folders) == 0
	a.mu.Unlock()
	if none {
		a.mu.Lock()
		a.view = viewSettings
		a.status = "Add a music folder to get started."
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
			albums, err := a.lib.Albums(ctx, artist.ID)
			a.finishLoad(func() { a.albums, a.loading = albums, false }, err)
			return
		}
	case levelTracks:
		if album != nil {
			tracks, err := a.lib.AlbumTracks(ctx, album)
			a.finishLoad(func() { a.tracks, a.loading = tracks, false }, err)
			a.probeDurations()
			return
		}
	}
	artists, err := a.lib.Artists(ctx)
	a.finishLoad(func() { a.artists, a.loading = artists, false }, err)
}

// finishLoad applies a loaded result under the lock and reports a failure in the
// status line. A load that fails must not look like an empty library.
func (a *App) finishLoad(apply func(), err error) {
	a.mu.Lock()
	a.loading = false
	if err != nil {
		a.status = "could not read the library: " + err.Error()
	} else {
		apply()
	}
	a.mu.Unlock()
	a.win.Invalidate()
}

// drill opens an artist, loading their albums in the background.
func (a *App) drillArtist(ar *library.Artist) {
	a.mu.Lock()
	a.artist, a.album, a.level, a.albums = ar, nil, levelAlbums, nil
	a.mu.Unlock()
	a.list.Position = layout.Position{}
	go a.reload()
}

// drillAlbum opens an album, loading its tracks in the background.
func (a *App) drillAlbum(al *library.Album) {
	a.mu.Lock()
	a.album, a.level, a.tracks = al, levelTracks, nil
	a.mu.Unlock()
	a.list.Position = layout.Position{}
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
	s := fmt.Sprintf("%d tracks in %d folder(s)", tracks, len(folders))
	if failed > 0 {
		s += fmt.Sprintf(" · %d unreadable", failed)
	}
	if missing > 0 {
		s += fmt.Sprintf(" · %d folder(s) not connected", missing)
	}
	return s
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
			if t.Duration == 0 && t.Available() && player.Decodable(t.Path) {
				todo = append(todo, t)
			}
		}
		a.mu.Unlock()

		for i, t := range todo {
			d, err := player.Probe(t.Path)
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
func (a *App) Run() error {
	var ops op.Ops
	tick := time.NewTicker(a.pl.Tick())
	defer tick.Stop()
	go func() {
		for range tick.C {
			if a.pl.Playing() {
				a.win.Invalidate()
			}
		}
	}()

	for {
		switch e := a.win.Event().(type) {
		case app.DestroyEvent:
			a.save()
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

func (a *App) layout(gtx C) D {
	a.update(gtx)
	paint.FillShape(gtx.Ops, colBg, clip.Rect{Max: gtx.Constraints.Max}.Op())

	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(a.header),
		layout.Flexed(1, a.body),
		layout.Rigid(a.playerBar),
	)
}

// update handles every control before anything is laid out, so a click and the
// frame it affects are the same frame.
func (a *App) update(gtx C) {
	if a.btnSettings.Clicked(gtx) {
		a.view = toggleView(a.view, viewSettings)
	}
	if a.btnQueue.Clicked(gtx) {
		a.view = toggleView(a.view, viewQueue)
	}
	if a.btnClearQueue.Clicked(gtx) {
		a.pl.ClearQueue()
	}

	// Escape steps back one level: it closes the search first, then walks the
	// drill up. This is the same order the web UI's back handler uses, and it is
	// what a phone's hardware back button will need when this reaches Android.
	for {
		ev, ok := gtx.Event(key.Filter{Name: key.NameEscape})
		if !ok {
			break
		}
		if ke, isKey := ev.(key.Event); !isKey || ke.State != key.Press {
			continue
		}
		switch {
		case a.view != viewBrowse:
			a.search.SetText("")
			a.view = viewBrowse
		default:
			a.drillUp()
		}
	}
	if a.crumbHome.Clicked(gtx) {
		a.level, a.artist, a.album = levelArtists, nil, nil
		go a.reload()
	}
	if a.crumbArt.Clicked(gtx) {
		a.level, a.album = levelAlbums, nil
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
		a.notice = err.Error()
	}
}

// doSearch runs a search in the background. The query is re-checked on arrival:
// a slower answer for text the user has already replaced is dropped rather than
// painted over the newer one.
func (a *App) doSearch(q string) {
	res, err := a.lib.Search(context.Background(), q)
	a.mu.Lock()
	if err != nil {
		a.status = "search failed: " + err.Error()
	} else if a.search.Text() == q {
		a.found = res
	}
	a.mu.Unlock()
	a.win.Invalidate()
}

func toggleView(cur, want view) view {
	if cur == want {
		return viewBrowse
	}
	return want
}

func (a *App) drillUp() bool {
	switch a.level {
	case levelTracks:
		a.level, a.album = levelAlbums, nil
		go a.reload()
		return true
	case levelAlbums:
		a.level, a.artist = levelArtists, nil
		go a.reload()
		return true
	}
	return false
}

func (a *App) header(gtx C) D {
	return bar(gtx, func(gtx C) D {
		return layout.Inset{Top: 10, Bottom: 10, Left: 16, Right: 16}.Layout(gtx, func(gtx C) D {
			return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx C) D {
					l := material.H6(a.th, "madplayer")
					l.Color = colFg
					return l.Layout(gtx)
				}),
				layout.Rigid(layout.Spacer{Width: 16}.Layout),
				layout.Flexed(1, func(gtx C) D {
					ed := material.Editor(a.th, &a.search, "Search artists, albums, tracks…")
					ed.Color, ed.HintColor = colFg, colDim
					return filled(gtx, colSel, ed.Layout)
				}),
				layout.Rigid(layout.Spacer{Width: 8}.Layout),
				layout.Rigid(func(gtx C) D {
					return a.smallButton(gtx, &a.btnQueue, fmt.Sprintf("Queue (%d)", a.pl.QueueLen()), a.view == viewQueue)
				}),
				layout.Rigid(layout.Spacer{Width: 8}.Layout),
				layout.Rigid(func(gtx C) D {
					return a.smallButton(gtx, &a.btnSettings, "Folders", a.view == viewSettings)
				}),
			)
		})
	})
}

func (a *App) body(gtx C) D {
	switch a.view {
	case viewSettings:
		return a.settings(gtx)
	case viewQueue:
		return a.queuePanel(gtx)
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
			r := gtx.Dp(6)
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
	if active {
		b.Background = colAccent
	}
	b.Color = colFg
	b.CornerRadius = 6
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
func (a *App) itemsFromTracks(tracks []*library.Track) []*queue.Item {
	out := make([]*queue.Item, len(tracks))
	for i, t := range tracks {
		out[i] = &queue.Item{
			Path:     t.Path,
			Title:    t.Title,
			Artist:   t.Artist,
			Album:    t.Album,
			Duration: t.Duration,
		}
	}
	return out
}

func (a *App) setStatus(msg string) {
	a.mu.Lock()
	a.status = msg
	a.mu.Unlock()
	a.win.Invalidate()
}
