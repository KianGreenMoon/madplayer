package ui

import (
	"fmt"
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
}

func (a *App) browse(gtx C) D {
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(a.breadcrumb),
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

// breadcrumb is hidden at the artist level — there is nothing to step back to,
// and an empty strip is worse than no strip.
func (a *App) breadcrumb(gtx C) D {
	if a.level == levelArtists {
		return D{}
	}
	return layout.Inset{Top: 10, Bottom: 10, Left: 20, Right: 20}.Layout(gtx, func(gtx C) D {
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
	})
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
			// shape, no error (docs/ui/madplayer.md §"The browse endpoints
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
	albums, loading := a.albums, a.loading
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
		return a.trackRow(gtx, &a.rows[i], e.track, e.index+1)
	})
}

// playFrom makes the clicked view the queue, in the order shown — browsing
// never changes the queue, only a click or an explicit edit does.
func (a *App) playFrom(tracks []*library.Track, index int) {
	want := tracks[index]
	if !want.Available() {
		a.notice = want.Title + " is not on this device right now"
		return
	}
	cur := a.pl.Current()
	if cur != nil && cur.RowKey() == rowKey(want) && a.pl.Playing() {
		a.pl.Toggle() // clicking the playing row toggles pause
		return
	}
	if a.pl.SetQueue(a.itemsFromTracks(tracks), index) {
		a.notice = "Queue replaced — Undo to restore it"
	}
}

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
	name := library.FileName(c.URL)
	if name == "" || !strings.Contains(name, ".") {
		return true // nothing to judge by; let playing it be the answer
	}
	return player.Decodable(name)
}

// originBadge says which library a row came from.
//
// It renders NOTHING when this device is the only library there is, which is the
// normal case and the offline player's whole posture: a badge on every row
// saying "this device" would be noise about a distinction that does not yet
// exist for that person.
func (a *App) originBadge(gtx C, origins []library.Origin) D {
	if !a.lib.Remote() || len(origins) == 0 {
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
	return fmt.Sprintf("%d tracks", n)
}

func (a *App) discHeader(gtx C, txt string) D {
	return layout.Inset{Top: 14, Bottom: 6, Left: 20, Right: 20}.Layout(gtx, func(gtx C) D {
		l := material.Caption(a.th, txt)
		l.Color = colDim
		return l.Layout(gtx)
	})
}

// trackRow renders number, title, performer and duration.
//
// The per-row artist is the PERFORMER — the track's own credit, not the album
// artist — which is what makes a compilation readable.
func (a *App) trackRow(gtx C, click *widget.Clickable, t *library.Track, fallbackNum int) D {
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
			layout.Rigid(func(gtx C) D { return a.rowMeta(gtx, t.DurationString()) }),
		)
	})
}

// --- search -----------------------------------------------------------------

// searchResults renders the three sections. An artist or album hit DRILLS (and
// clears the search); a track hit PLAYS.
func (a *App) searchResults(gtx C) D {
	res := a.found
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
						return a.rowMeta(gtx, fmt.Sprintf("%d tracks", it.artist.TrackCount))
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
			return a.trackRow(gtx, &a.rows[i], it.track, 0)
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
