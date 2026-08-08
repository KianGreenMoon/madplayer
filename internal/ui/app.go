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

	"daemonlord.ygg/madplayer/internal/library"
	"daemonlord.ygg/madplayer/internal/player"
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
type App struct {
	win   *app.Window
	th    *material.Theme
	store *library.Store
	pl    *player.Player

	// mu guards everything a background scan or probe pass writes.
	mu       sync.Mutex
	cfg      library.Config
	tracks   []*library.Track
	idx      *library.Index
	scanning bool
	status   string
	notice   string

	view  view
	level level

	artist *library.Artist
	album  *library.Album
	found  library.SearchResults

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

	scanCancel context.CancelFunc
}

// New wires the UI to a player and loads whatever the last run indexed.
func New(win *app.Window, pl *player.Player) *App {
	a := &App{win: win, th: newTheme(), store: library.DefaultStore(), pl: pl}
	a.list.Axis = layout.Vertical
	a.queueList.Axis = layout.Vertical
	a.search.SingleLine = true
	a.folderEd.SingleLine = true

	cfg, err := a.store.LoadConfig()
	if err != nil {
		a.status = "settings could not be read: " + err.Error()
	}
	a.cfg = cfg
	a.vol.Value = float32(cfg.Volume)
	pl.SetVolume(cfg.Volume)

	// Start from the cached index so the library is browsable immediately; a
	// rescan then reconciles it with the disk in the background.
	a.setTracks(a.store.LoadTracks())
	if len(a.cfg.Roots) == 0 {
		a.view = viewSettings
		a.status = "Add a music folder to get started."
	} else {
		a.Rescan()
	}

	// The player advances the queue from its own goroutine, so a repaint has to
	// be asked for rather than assumed.
	pl.OnChange = func() { win.Invalidate() }
	return a
}

func (a *App) setTracks(tracks []*library.Track) {
	a.mu.Lock()
	a.tracks = tracks
	a.idx = library.Build(tracks)
	a.mu.Unlock()
}

// index returns the current index; never nil.
func (a *App) index() *library.Index {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.idx == nil {
		a.idx = library.Build(nil)
	}
	return a.idx
}

// Rescan walks the configured folders in the background.
func (a *App) Rescan() {
	a.mu.Lock()
	if a.scanning {
		a.mu.Unlock()
		return
	}
	roots := append([]string(nil), a.cfg.Roots...)
	prev := library.TrackMap(a.tracks)
	a.scanning = true
	a.status = "Scanning…"
	ctx, cancel := context.WithCancel(context.Background())
	a.scanCancel = cancel
	a.mu.Unlock()
	a.win.Invalidate()

	go func() {
		defer cancel()
		tracks, sum := library.Scan(ctx, roots, prev, func(s library.ScanSummary) {
			a.mu.Lock()
			a.status = fmt.Sprintf("Scanning… %d files", s.Scanned)
			a.mu.Unlock()
			a.win.Invalidate()
		})

		a.setTracks(tracks)
		a.mu.Lock()
		a.scanning = false
		a.status = describeScan(sum, len(tracks))
		a.mu.Unlock()

		if err := a.store.SaveTracks(tracks); err != nil {
			a.mu.Lock()
			a.status += " (index not saved: " + err.Error() + ")"
			a.mu.Unlock()
		}
		a.win.Invalidate()
		a.probeDurations()
	}()
}

func describeScan(sum library.ScanSummary, total int) string {
	s := fmt.Sprintf("%d tracks", total)
	if sum.Added > 0 {
		s += fmt.Sprintf(" · %d new", sum.Added)
	}
	if sum.Updated > 0 {
		s += fmt.Sprintf(" · %d changed", sum.Updated)
	}
	// Failures are named, not just counted: a scan that quietly indexed 9 of 10
	// files is a scan that lost one.
	if sum.Failed > 0 {
		s += fmt.Sprintf(" · %d unreadable", sum.Failed)
		if len(sum.Errors) > 0 {
			s += " (" + sum.Errors[0] + ")"
		}
	}
	return s
}

// probeDurations fills in lengths the tags did not carry.
//
// It runs AFTER the list is already on screen, which is the rule
// docs/ui/library-page.md sets: the row renders immediately with "—" rather than
// the library waiting on a walk over every file.
func (a *App) probeDurations() {
	go func() {
		a.mu.Lock()
		todo := make([]*library.Track, 0, 64)
		for _, t := range a.tracks {
			if t.Duration == 0 && player.Decodable(t.Path) {
				todo = append(todo, t)
			}
		}
		a.mu.Unlock()
		if len(todo) == 0 {
			return
		}

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
		a.mu.Lock()
		tracks := a.tracks
		a.mu.Unlock()
		_ = a.store.SaveTracks(tracks)
		a.win.Invalidate()
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
	_ = a.store.SaveConfig(cfg)
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
	}
	if a.crumbArt.Clicked(gtx) {
		a.level, a.album = levelAlbums, nil
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
				a.found = a.index().Search(q)
				a.view = viewSearch
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
		return true
	case levelAlbums:
		a.level, a.artist = levelArtists, nil
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
// text so the queue survives the index being rebuilt under it.
func (a *App) itemsFromTracks(tracks []*library.Track) []*queue.Item {
	ix := a.index()
	out := make([]*queue.Item, len(tracks))
	for i, t := range tracks {
		album := ""
		if al := ix.Album(t.AlbumID); al != nil {
			album = al.Title
		}
		out[i] = &queue.Item{
			Path:     t.Path,
			Title:    t.DisplayTitle(),
			Artist:   ix.ArtistName(t),
			Album:    album,
			Duration: t.Duration,
		}
	}
	return out
}
