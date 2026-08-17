package ui

import (
	"fmt"
	"image"
	"strings"

	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"daemonlord.ygg/madplayer/internal/library"
	"daemonlord.ygg/madplayer/internal/player"
)

// entry is one line of the drill panel. A disc header is an entry too, but it
// carries no track — headers are VISUAL ONLY, and `index` keeps pointing at the
// flat track list so a header can never shift what a click plays.
type entry struct {
	header string
	track  *library.Track
	index  int
}

func (a *App) ensureRows(n int) {
	if len(a.rows) < n {
		a.rows = append(a.rows, make([]widget.Clickable, n-len(a.rows))...)
	}
	// The per-row queue buttons are grown with the rows they sit on, so an index
	// that is valid for one is valid for all three.
	if len(a.rowNext) < n {
		a.rowNext = append(a.rowNext, make([]widget.Clickable, n-len(a.rowNext))...)
		a.rowAdd = append(a.rowAdd, make([]widget.Clickable, n-len(a.rowAdd))...)
		a.rowKeep = append(a.rowKeep, make([]widget.Clickable, n-len(a.rowKeep))...)
	}
}

func (a *App) browse(gtx C) D {
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(a.browseBar),
		layout.Flexed(1, func(gtx C) D {
			switch a.level {
			case levelAlbums:
				return a.albumList(gtx)
			case levelTracks:
				return a.trackList(gtx)
			default:
				return a.artistList(gtx)
			}
		}),
	)
}

// browseBar is the strip above the rows: where you are, and how much is being
// listed.
//
// It is drawn only when it has something to say. At the artist level with
// nothing but this device's music there is neither a trail to walk back nor a
// scope to narrow, and an empty strip is worse than no strip.
func (a *App) browseBar(gtx C) D {
	crumbs, scope := a.level != levelArtists, a.lib.Remote()
	if !crumbs && !scope {
		return D{}
	}
	return layout.Inset{Top: 10, Bottom: 10, Left: 20, Right: 20}.Layout(gtx, func(gtx C) D {
		return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
			layout.Flexed(1, func(gtx C) D {
				if !crumbs {
					return D{}
				}
				return a.breadcrumb(gtx)
			}),
			layout.Rigid(func(gtx C) D {
				if !scope {
					return D{}
				}
				return a.smallButton(gtx, &a.btnLocalOnly, a.scopeLabel(),
					a.lib.Scope() == library.ScopeDevice)
			}),
		)
	})
}

// breadcrumb is the trail back up the drill. It is laid out by browseBar, which
// decides whether there is anything to show at all.
func (a *App) breadcrumb(gtx C) D {
	if a.level == levelArtists {
		return D{}
	}
	children := []layout.FlexChild{
		layout.Rigid(func(gtx C) D { return a.crumb(gtx, &a.crumbHome, "Library", true) }),
	}
	if a.artist != nil {
		children = append(children,
			layout.Rigid(func(gtx C) D { return a.crumbSep(gtx) }),
			layout.Rigid(func(gtx C) D {
				return a.crumb(gtx, &a.crumbArt, a.artist.Name, a.level != levelAlbums)
			}),
		)
	}
	if a.album != nil {
		children = append(children,
			layout.Rigid(func(gtx C) D { return a.crumbSep(gtx) }),
			layout.Rigid(func(gtx C) D {
				l := material.Body2(a.th, a.album.Title)
				l.Color = colFg
				l.MaxLines = 1
				return l.Layout(gtx)
			}),
		)
	}
	return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx, children...)
}

func (a *App) crumb(gtx C, click *widget.Clickable, txt string, link bool) D {
	if !link {
		l := material.Body2(a.th, txt)
		l.Color = colFg
		l.MaxLines = 1
		return l.Layout(gtx)
	}
	return click.Layout(gtx, func(gtx C) D {
		l := material.Body2(a.th, txt)
		l.Color = colAccent
		l.MaxLines = 1
		return l.Layout(gtx)
	})
}

func (a *App) crumbSep(gtx C) D {
	l := material.Body2(a.th, "  ›  ")
	l.Color = colDim
	return l.Layout(gtx)
}

// --- artists ----------------------------------------------------------------

func (a *App) artistList(gtx C) D {
	a.mu.Lock()
	artists := a.artists
	scanning, loading, folders := a.scanning, a.loading, len(a.folders)
	a.mu.Unlock()
	if len(artists) == 0 {
		switch {
		case scanning:
			return a.emptyState(gtx, "Scanning…")
		case loading:
			return a.emptyState(gtx, "Reading your library…")
		case folders == 0 && !a.lib.Remote():
			return a.emptyState(gtx, "No music folders yet. Open Settings to add one, or Servers to sign in to one.")
		case a.lib.Remote():
			// A server answering with nothing is not the same as having nothing:
			// an account without content.access gets the guest listing, same
			// shape, no error (docs/design.md §"The browse endpoints
			// narrow, they do not refuse").
			return a.emptyState(gtx, "Nothing to show. Your folders are empty, and the servers you are "+
				"signed in to returned nothing your account may see.")
		default:
			return a.emptyState(gtx, "No music found in your folders.")
		}
	}

	a.ensureRows(len(artists))
	lst := material.List(a.th, &a.list)
	lst.Indicator.Color = colLine
	return lst.Layout(gtx, len(artists), func(gtx C, i int) D {
		ar := artists[i]
		if a.rows[i].Clicked(gtx) {
			a.drillArtist(ar)
		}
		return a.row(gtx, &a.rows[i], false, func(gtx C) D {
			return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
				layout.Flexed(1, func(gtx C) D { return a.rowTitle(gtx, ar.Name, false) }),
				layout.Rigid(func(gtx C) D { return a.originBadge(gtx, ar.Origins) }),
				layout.Rigid(func(gtx C) D {
					return a.rowMeta(gtx, countText(ar.TrackCount, ar.Approx))
				}),
				layout.Rigid(func(gtx C) D { return a.chevron(gtx) }),
			)
		})
	})
}

// --- albums -----------------------------------------------------------------

func (a *App) albumList(gtx C) D {
	if a.artist == nil {
		a.level = levelArtists
		return D{}
	}
	a.mu.Lock()
	albums, loading, art := a.albums, a.loading, a.albumArt
	a.mu.Unlock()
	if len(albums) == 0 {
		if loading {
			return a.emptyState(gtx, "Reading…")
		}
		return a.emptyState(gtx, "No albums found.")
	}

	a.ensureRows(len(albums))
	lst := material.List(a.th, &a.list)
	lst.Indicator.Color = colLine
	return lst.Layout(gtx, len(albums), func(gtx C, i int) D {
		al := albums[i]
		if a.rows[i].Clicked(gtx) {
			a.drillAlbum(al)
		}
		meta := countText(al.TrackCount, al.Approx)
		if al.Year > 0 {
			meta = fmt.Sprintf("%d · %s", al.Year, meta)
		}
		return a.row(gtx, &a.rows[i], false, func(gtx C) D {
			return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx C) D { return a.cover(gtx, art[al], 40) }),
				layout.Rigid(layout.Spacer{Width: 12}.Layout),
				layout.Flexed(1, func(gtx C) D { return a.rowTitle(gtx, al.Title, false) }),
				layout.Rigid(func(gtx C) D { return a.originBadge(gtx, al.Origins) }),
				layout.Rigid(func(gtx C) D { return a.rowMeta(gtx, meta) }),
				layout.Rigid(func(gtx C) D { return a.chevron(gtx) }),
			)
		})
	})
}

// --- tracks -----------------------------------------------------------------

// albumEntries interleaves disc headers with tracks. An album spanning more than
// one distinct disc gets "Disc N" separators; untagged, 0 and N are three
// distinct discs.
func albumEntries(tracks []*library.Track) []entry {
	multi := library.IsMultiDisc(tracks)
	out := make([]entry, 0, len(tracks)+2)
	var last library.DiscKey
	for i, t := range tracks {
		k := library.KeyOfDisc(t.DiscNumber)
		if multi && (i == 0 || k != last) {
			out = append(out, entry{header: k.Label(), index: -1})
		}
		last = k
		out = append(out, entry{track: t, index: i})
	}
	return out
}

func (a *App) trackList(gtx C) D {
	if a.album == nil {
		a.level = levelAlbums
		return D{}
	}
	// An album always opens WHOLE: it is addressed by id, and an id names the
	// release, not a slice of it — so a compilation reached from a performer's
	// album list shows every track even though the row that led here counted
	// only theirs. The breadcrumb naming the album artist is the explanation.
	a.mu.Lock()
	tracks, loading := a.tracks, a.loading
	a.mu.Unlock()
	if len(tracks) == 0 {
		if loading {
			return a.emptyState(gtx, "Reading…")
		}
		return a.emptyState(gtx, "No tracks found.")
	}

	entries := albumEntries(tracks)
	a.ensureRows(len(entries))
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx C) D { return a.albumHeader(gtx, tracks) }),
		layout.Flexed(1, func(gtx C) D {
			lst := material.List(a.th, &a.list)
			lst.Indicator.Color = colLine
			return lst.Layout(gtx, len(entries), func(gtx C, i int) D {
				e := entries[i]
				if e.track == nil {
					return a.discHeader(gtx, e.header)
				}
				if a.rows[i].Clicked(gtx) {
					a.playFrom(tracks, e.index)
				}
				return a.trackRow(gtx, i, e.track, e.index+1)
			})
		}),
	)
}

// albumHeader is the sleeve above an album's tracks: the cover, what the record
// is, and one button to start it.
//
// The cover comes from the first track this machine holds — the same rule the
// album rows use — so a server-only album shows the placeholder rather than a
// gap. It is the one place large enough for art to be worth showing at size.
func (a *App) albumHeader(gtx C, tracks []*library.Track) D {
	if a.btnPlayAlbum.Clicked(gtx) {
		a.playFrom(tracks, 0)
	}
	// The album's three actions are the same three a track row offers, spelled
	// out. That is what teaches the two icons on the rows below.
	if a.btnAlbumNext.Clicked(gtx) {
		a.enqueue(tracks, true)
	}
	if a.btnAlbumAdd.Clicked(gtx) {
		a.enqueue(tracks, false)
	}
	if a.btnAlbumKeep.Clicked(gtx) {
		a.keep(tracks, a.albumArtistName())
	}
	total := 0.0
	for _, t := range tracks {
		total += t.Duration
	}
	meta := plural(len(tracks), "track")
	if total > 0 {
		meta += "  ·  " + clock(total)
	}
	if a.album.Year > 0 {
		meta = fmt.Sprintf("%d  ·  %s", a.album.Year, meta)
	}

	titles := func(gtx C) D {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx C) D {
				l := material.H6(a.th, a.album.Title)
				l.Color = colFg
				l.MaxLines = 2
				return l.Layout(gtx)
			}),
			layout.Rigid(func(gtx C) D {
				l := material.Body2(a.th, a.album.ArtistName)
				l.Color = colDim
				l.MaxLines = 1
				return l.Layout(gtx)
			}),
			layout.Rigid(layout.Spacer{Height: 6}.Layout),
			layout.Rigid(func(gtx C) D {
				l := material.Caption(a.th, meta)
				l.Color = colDim
				l.MaxLines = 1
				return l.Layout(gtx)
			}),
		)
	}
	queueActions := func(gtx C) D {
		return layout.Flex{Axis: layout.Horizontal}.Layout(gtx,
			layout.Rigid(func(gtx C) D {
				return a.smallButton(gtx, &a.btnPlayAlbum, "Play", false)
			}),
			layout.Rigid(layout.Spacer{Width: 8}.Layout),
			layout.Rigid(func(gtx C) D {
				return a.smallButton(gtx, &a.btnAlbumNext, "Play next", false)
			}),
			layout.Rigid(layout.Spacer{Width: 8}.Layout),
			layout.Rigid(func(gtx C) D {
				return a.smallButton(gtx, &a.btnAlbumAdd, "Add to queue", false)
			}),
		)
	}

	return layout.Inset{Top: 4, Bottom: 14, Left: 20, Right: 20}.Layout(gtx, func(gtx C) D {
		// The action row next to a 104 dp cover has about 250 dp on a phone,
		// which three buttons overflow and a fourth never entered — "Keep on
		// this device" was simply off the screen. So below narrowBar the
		// actions leave the cover's column: the queue actions take a full-width
		// row under the header, and the keep offer a row under those. The keep
		// button cannot share their row even at full width — its label is a
		// sentence, and honestly counting ("Keep 7 on this device") makes it
		// longer still.
		if gtx.Constraints.Max.X < gtx.Dp(narrowBar) {
			children := []layout.FlexChild{
				layout.Rigid(func(gtx C) D {
					return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(func(gtx C) D { return a.cover(gtx, albumCoverPath(tracks), 104) }),
						layout.Rigid(layout.Spacer{Width: 16}.Layout),
						layout.Flexed(1, titles),
					)
				}),
				layout.Rigid(layout.Spacer{Height: 10}.Layout),
				layout.Rigid(queueActions),
			}
			if keep := a.albumKeepButton(tracks); keep != nil {
				children = append(children,
					layout.Rigid(layout.Spacer{Height: 8}.Layout),
					layout.Rigid(keep),
				)
			}
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
		}
		return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx C) D { return a.cover(gtx, albumCoverPath(tracks), 104) }),
			layout.Rigid(layout.Spacer{Width: 16}.Layout),
			layout.Flexed(1, func(gtx C) D {
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Rigid(titles),
					layout.Rigid(layout.Spacer{Height: 10}.Layout),
					layout.Rigid(func(gtx C) D {
						keep := a.albumKeepButton(tracks)
						if keep == nil {
							return queueActions(gtx)
						}
						return layout.Flex{Axis: layout.Horizontal}.Layout(gtx,
							layout.Rigid(queueActions),
							layout.Rigid(layout.Spacer{Width: 8}.Layout),
							layout.Rigid(keep),
						)
					}),
				)
			}),
		)
	})
}

// playFrom makes the clicked view the queue, in the order shown — browsing
// never changes the queue, only a click or an explicit edit does.
func (a *App) playFrom(tracks []*library.Track, index int) {
	want := tracks[index]
	if !want.Available() {
		a.setNotice(want.Title + " is not on this device right now")
		return
	}
	cur := a.pl.Current()
	if cur != nil && cur.RowKey() == rowKey(want) && a.pl.Playing() {
		a.pl.Toggle() // clicking the playing row toggles pause
		return
	}
	if a.pl.SetQueue(a.itemsFromTracks(tracks), index) {
		a.setNotice("Queue replaced — Undo to restore it")
	}
}

// enqueue adds tracks to the queue WITHOUT replacing it: right after what is
// playing, or at the end.
//
// This is the other half of clicking a row, and the reason it had to exist: with
// only playFrom, every way of choosing music threw away the queue you had built.
// It follows docs/ui/player-and-queue.md — both mark the queue dirty, and
// neither disturbs what is playing, including on an empty queue, where adding
// starts nothing. The web UI does the same, and a queue that silently began
// playing because something was added to it would be a different program.
func (a *App) enqueue(tracks []*library.Track, next bool) {
	playable := make([]*library.Track, 0, len(tracks))
	for _, t := range tracks {
		if t.Available() {
			playable = append(playable, t)
		}
	}
	if len(playable) == 0 {
		a.setNotice("Nothing there to queue — those tracks are not on this device right now")
		return
	}

	items := a.itemsFromTracks(playable)
	if next {
		a.pl.PlayNext(items...)
	} else {
		a.pl.Append(items...)
	}
	a.setNotice(enqueueNotice(len(playable), len(tracks), next))
}

// enqueueNotice says what was added and — when they differ — how many were left
// out. A silent partial add on an album with one unplugged drive in it is the
// case worth naming.
func enqueueNotice(added, asked int, next bool) string {
	where := "added to the queue"
	if next {
		where = "playing next"
	}
	what := plural(added, "track")
	if skipped := asked - added; skipped > 0 {
		return fmt.Sprintf("%s %s — %d not on this device right now", what, where, skipped)
	}
	return what + " " + where
}

// rowActions are the two queue buttons on a track row. They appear on hover, so
// a list of music is a list of music rather than a wall of controls, and the
// space is reserved either way — buttons that push the duration column sideways
// as the pointer moves are worse than buttons that are always there.
//
// Hover is read from the row AND from the buttons themselves: moving the pointer
// onto a button leaves the row's own area, and without this the controls would
// vanish exactly as they were reached.
func (a *App) rowActions(gtx C, i int, t *library.Track) D {
	if a.rowNext[i].Clicked(gtx) {
		a.enqueue([]*library.Track{t}, true)
	}
	if a.rowAdd[i].Clicked(gtx) {
		a.enqueue([]*library.Track{t}, false)
	}

	// Keeping is offered only for music that is somewhere else. A track already
	// on this device has nothing to keep, and a button saying otherwise would be
	// a button that does nothing.
	keepable := a.keepable(t)
	if keepable && a.rowKeep[i].Clicked(gtx) {
		a.keep([]*library.Track{t}, a.albumArtistName())
	}

	// On a phone there is no pointer to hover with — the buttons were
	// invisible until already pressed, which is a control surface nobody can
	// discover. At phone width they are simply always there (owner's call,
	// 2026-08-17); the hover reveal stays the desktop's tidier answer.
	show := a.narrowUI ||
		a.rows[i].Hovered() || a.rowNext[i].Hovered() || a.rowAdd[i].Hovered() || a.rowKeep[i].Hovered()
	width := 2*rowActionSize + 8
	if keepable {
		width += rowActionSize + 8
	}
	if !show {
		return D{Size: image.Pt(gtx.Dp(width), 0)}
	}
	children := []layout.FlexChild{
		layout.Rigid(func(gtx C) D { return a.iconButton(gtx, &a.rowNext[i], iconPlayNext, rowActionSize) }),
		layout.Rigid(layout.Spacer{Width: 8}.Layout),
		layout.Rigid(func(gtx C) D { return a.iconButton(gtx, &a.rowAdd[i], iconAddQueue, rowActionSize) }),
	}
	if keepable {
		children = append(children,
			layout.Rigid(layout.Spacer{Width: 8}.Layout),
			layout.Rigid(func(gtx C) D { return a.iconButton(gtx, &a.rowKeep[i], iconKeep, rowActionSize) }),
		)
	}
	return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx, children...)
}

// albumArtistName is the album artist when an album is open, and "" otherwise —
// which is what a search hit is, and what makes keepTrack fall back to the
// track's own credit.
func (a *App) albumArtistName() string {
	if a.album == nil {
		return ""
	}
	return a.album.ArtistName
}

// rowActionSize is the side of a row's icon buttons.
const rowActionSize = unit.Dp(26)

// trackProblem is why a row cannot be played, in the user's terms — and the
// reasons are deliberately different sentences. Bytes nothing can reach are
// somebody's unplugged drive; a container Go has no decoder for is this
// program's own limit. Reporting either as "missing" would be a lie about a file
// that is perfectly fine.
func trackProblem(pl *player.Player, t *library.Track) string {
	best, ok := t.Best()
	switch {
	case !ok:
		return "not on this device right now"
	case pl.Unplayable(rowKey(t)) != nil:
		return "cannot be played"
	case !decodableCopy(best):
		return "cannot be played"
	}
	return ""
}

// decodableCopy reports whether this build has a decoder for a copy's container.
//
// A remote copy is judged by its file name, which is all there is to go on
// before downloading it — and downloading a track to discover there is no
// decoder for it is exactly the waste worth avoiding.
func decodableCopy(c library.Copy) bool {
	if c.Path != "" {
		return player.Decodable(c.Path)
	}
	// A madnetwork copy has no name to read, and does not need one: the
	// catalogue states the codec, which is the fact the question is really about.
	ext := c.Ext()
	if ext == "" {
		return true // nothing to judge by; let playing it be the answer
	}
	return player.Decodable("audio" + ext)
}

// originBadge says which library a row came from.
//
// It renders NOTHING when this device is the only library there is, which is the
// normal case and the offline player's whole posture: a badge on every row
// saying "this device" would be noise about a distinction that does not yet
// exist for that person.
func (a *App) originBadge(gtx C, origins []library.Origin) D {
	// At phone width the badge is dropped entirely (owner's call, 2026-08-17):
	// a 400 dp row has one line to spend and the title is what it is for.
	if a.narrowUI || !a.lib.Remote() || len(origins) == 0 {
		return D{}
	}
	return layout.Inset{Left: 10}.Layout(gtx, func(gtx C) D {
		l := material.Caption(a.th, originText(origins))
		l.Color = colDim
		l.MaxLines = 1
		return l.Layout(gtx)
	})
}

// originText names the libraries a row is in. Two are named; more are counted,
// because a row is not a place to list six servers.
func originText(origins []library.Origin) string {
	labels := make([]string, 0, len(origins))
	seen := map[string]bool{}
	for _, o := range origins {
		if seen[o.Source] {
			continue
		}
		seen[o.Source] = true
		labels = append(labels, o.Label)
	}
	switch len(labels) {
	case 0:
		return ""
	case 1, 2:
		return strings.Join(labels, " + ")
	}
	return fmt.Sprintf("%d libraries", len(labels))
}

// countText renders a merged count. The "+" is not decoration: a count merged
// from two libraries is a LOWER BOUND, because the same track held in both is
// one row here and two rows there (see library/merge.go countFrom).
func countText(n int, approx bool) string {
	if approx {
		return fmt.Sprintf("%d+ tracks", n)
	}
	return plural(n, "track")
}

func (a *App) discHeader(gtx C, txt string) D {
	return layout.Inset{Top: 14, Bottom: 6, Left: 20, Right: 20}.Layout(gtx, func(gtx C) D {
		l := material.Caption(a.th, txt)
		l.Color = colDim
		return l.Layout(gtx)
	})
}

// trackRow renders number, title, performer, the queue buttons and duration.
//
// The per-row artist is the PERFORMER — the track's own credit, not the album
// artist — which is what makes a compilation readable.
//
// It takes the row's INDEX rather than its clickable, because a row now owns
// three widgets and one index is what keeps them pointing at the same track.
func (a *App) trackRow(gtx C, i int, t *library.Track, fallbackNum int) D {
	click := &a.rows[i]
	cur := a.pl.Current()
	key := rowKey(t)
	playing := key != "" && cur != nil && cur.RowKey() == key
	problem := trackProblem(a.pl, t)

	// The number is the tag when present, else the row's position, so a column
	// of numbers never has holes in it.
	num := t.TrackNumber
	if num == 0 {
		num = fallbackNum
	}

	return a.row(gtx, click, playing, func(gtx C) D {
		return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx C) D {
				gtx.Constraints.Min.X = gtx.Dp(34)
				l := material.Body2(a.th, fmt.Sprintf("%d", num))
				l.Color = colDim
				return l.Layout(gtx)
			}),
			layout.Flexed(1, func(gtx C) D {
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Rigid(func(gtx C) D { return a.rowTitle(gtx, t.Title, playing) }),
					layout.Rigid(func(gtx C) D {
						sub := t.Artist
						if problem != "" {
							sub += "  ·  " + problem
						}
						l := material.Caption(a.th, sub)
						l.Color = colDim
						if problem != "" {
							l.Color = colWarn
						}
						l.MaxLines = 1
						return l.Layout(gtx)
					}),
				)
			}),
			layout.Rigid(func(gtx C) D {
				if best, ok := t.Best(); ok {
					return a.originBadge(gtx, []library.Origin{best.Origin})
				}
				return D{}
			}),
			layout.Rigid(layout.Spacer{Width: 8}.Layout),
			layout.Rigid(func(gtx C) D { return a.rowActions(gtx, i, t) }),
			layout.Rigid(func(gtx C) D { return a.rowMeta(gtx, t.DurationString()) }),
		)
	})
}

// --- search -----------------------------------------------------------------

// searchResults renders the three sections. An artist or album hit DRILLS (and
// clears the search); a track hit PLAYS.
func (a *App) searchResults(gtx C) D {
	// Under the lock: doSearch writes this from a background goroutine, and a
	// layout function is not a place to read a slice somebody else is replacing.
	a.mu.Lock()
	res := a.found
	a.mu.Unlock()
	type item struct {
		section string
		artist  *library.Artist
		album   *library.Album
		track   *library.Track
	}
	var items []item
	if len(res.Artists) > 0 {
		items = append(items, item{section: "Artists"})
		for _, x := range res.Artists {
			items = append(items, item{artist: x})
		}
	}
	if len(res.Albums) > 0 {
		items = append(items, item{section: "Albums"})
		for _, x := range res.Albums {
			items = append(items, item{album: x})
		}
	}
	if len(res.Tracks) > 0 {
		items = append(items, item{section: "Tracks"})
		for _, x := range res.Tracks {
			items = append(items, item{track: x})
		}
	}
	if len(items) == 0 {
		return a.emptyState(gtx, "Nothing found.")
	}

	a.ensureRows(len(items))
	lst := material.List(a.th, &a.list)
	lst.Indicator.Color = colLine
	return lst.Layout(gtx, len(items), func(gtx C, i int) D {
		it := items[i]
		if it.section != "" {
			return a.discHeader(gtx, it.section)
		}
		clicked := a.rows[i].Clicked(gtx)
		switch {
		case it.artist != nil:
			if clicked {
				a.view = viewBrowse
				a.search.SetText("")
				a.drillArtist(it.artist)
			}
			return a.row(gtx, &a.rows[i], false, func(gtx C) D {
				return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
					layout.Flexed(1, func(gtx C) D { return a.rowTitle(gtx, it.artist.Name, false) }),
					layout.Rigid(func(gtx C) D {
						return a.rowMeta(gtx, plural(it.artist.TrackCount, "track"))
					}),
					layout.Rigid(func(gtx C) D { return a.chevron(gtx) }),
				)
			})
		case it.album != nil:
			if clicked {
				// The breadcrumb needs an artist to name, and the row carries the
				// album's own — which IS the album artist, so the crumb is right
				// without a query for it.
				a.artist = it.album.Artist()
				a.view = viewBrowse
				a.search.SetText("")
				a.drillAlbum(it.album)
			}
			return a.row(gtx, &a.rows[i], false, func(gtx C) D {
				return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
					layout.Flexed(1, func(gtx C) D { return a.rowTitle(gtx, it.album.Title, false) }),
					layout.Rigid(func(gtx C) D { return a.chevron(gtx) }),
				)
			})
		default:
			if clicked {
				a.playFrom(res.Tracks, indexOfTrack(res.Tracks, it.track))
			}
			return a.trackRow(gtx, i, it.track, 0)
		}
	})
}

func indexOfTrack(list []*library.Track, want *library.Track) int {
	for i, t := range list {
		if t == want {
			return i
		}
	}
	return 0
}

// --- row chrome -------------------------------------------------------------

func (a *App) row(gtx C, click *widget.Clickable, selected bool, w layout.Widget) D {
	return click.Layout(gtx, func(gtx C) D {
		return layout.Stack{}.Layout(gtx,
			layout.Expanded(func(gtx C) D {
				if selected {
					paint.FillShape(gtx.Ops, colSel, clip.Rect{Max: gtx.Constraints.Min}.Op())
				}
				return D{Size: gtx.Constraints.Min}
			}),
			layout.Stacked(func(gtx C) D {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				return layout.Inset{Top: 8, Bottom: 8, Left: 20, Right: 20}.Layout(gtx, w)
			}),
		)
	})
}

func (a *App) rowTitle(gtx C, txt string, accent bool) D {
	l := material.Body1(a.th, txt)
	l.MaxLines = 1
	l.Color = colFg
	if accent {
		l.Color = colAccent
	}
	return l.Layout(gtx)
}

func (a *App) rowMeta(gtx C, txt string) D {
	return layout.Inset{Left: 10}.Layout(gtx, func(gtx C) D {
		l := material.Body2(a.th, txt)
		l.Color = colDim
		l.TextSize = unit.Sp(13)
		return l.Layout(gtx)
	})
}

func (a *App) chevron(gtx C) D {
	return layout.Inset{Left: 10}.Layout(gtx, func(gtx C) D {
		l := material.Body2(a.th, "›")
		l.Color = colDim
		return l.Layout(gtx)
	})
}

// albumKeepButton offers to keep the whole album, and only when some of it is
// somewhere else — an album already on this device has nothing to keep, and
// that is the nil return, so the caller can lay out nothing rather than an
// empty widget's worth of spacing.
func (a *App) albumKeepButton(tracks []*library.Track) layout.Widget {
	remote := 0
	for _, t := range tracks {
		if a.keepable(t) {
			remote++
		}
	}
	if remote == 0 {
		return nil
	}
	a.mu.Lock()
	busy := a.keeping
	a.mu.Unlock()

	label := "Keep on this device"
	if remote < len(tracks) {
		// Naming the count is the honest version: half the album is already here,
		// and a button that says "keep the album" would be promising more work
		// than it is about to do.
		label = fmt.Sprintf("Keep %d on this device", remote)
	}
	if busy {
		label = "Keeping…"
	}
	return func(gtx C) D { return a.smallButton(gtx, &a.btnAlbumKeep, label, busy) }
}
