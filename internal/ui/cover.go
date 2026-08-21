package ui

import (
	"context"
	"errors"
	"image"
	"strings"
	"sync"
	"time"

	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"

	"daemonlord.ygg/madplayer/internal/artwork"
	"daemonlord.ygg/madplayer/internal/library"
	"daemonlord.ygg/madplayer/internal/player"
)

// Cover art on screen.
//
// The art itself is found by internal/artwork, which reads the audio file. This
// half is about painting it sixty times a second without doing any work: an
// image is turned into a paint.ImageOp ONCE and the op is kept, because
// paint.NewImageOp hands back a fresh handle each call and Gio's texture cache
// is keyed on the handle — building one per frame re-uploads the whole texture
// per frame.
//
// A cover is square here even when the file's is not. Album art is square by
// convention, the row heights depend on it, and letterboxing a 4:3 scan into a
// square tile looks like a bug; widget.Cover crops to fill instead, which is
// what every other player does.

// covers is the UI's side of the art: the decoded-image cache plus the uploaded
// ops built from it.
type covers struct {
	cache *artwork.Cache

	// fetchNet resolves a network cover key (netCoverPrefix-marked) to its
	// bytes. Set once at startup by the App, which is the layer that knows the
	// libraries; nil paints such keys as coverless rather than crashing.
	fetchNet func(key string) ([]byte, error)

	mu  sync.Mutex
	ops map[string]paint.ImageOp
}

// netCoverPrefix marks a cover key as network art rather than a file path.
// The prefix cannot open a file (no absolute path starts with it), so the two
// key spaces cannot collide however a library names things.
const netCoverPrefix = "net!"

func newCovers(invalidate func()) *covers {
	c := &covers{cache: artwork.New(), ops: map[string]paint.ImageOp{}}
	c.cache.OnLoad = invalidate
	return c
}

// op returns the paintable cover for an audio file, and whether there is one.
func (c *covers) op(path string) (paint.ImageOp, bool) {
	c.mu.Lock()
	if o, ok := c.ops[path]; ok {
		c.mu.Unlock()
		return o, true
	}
	c.mu.Unlock()

	var img image.Image
	var settled bool
	if strings.HasPrefix(path, netCoverPrefix) {
		if c.fetchNet == nil {
			return paint.ImageOp{}, false
		}
		img, settled = c.cache.GetFetched(path, func() ([]byte, error) { return c.fetchNet(path) })
	} else {
		img, settled = c.cache.Get(path)
	}
	if img == nil {
		// Either still reading or there is none; both paint the placeholder, and
		// settled is the caller's business rather than this one's.
		_ = settled
		return paint.ImageOp{}, false
	}

	o := paint.NewImageOp(img)
	c.mu.Lock()
	c.ops[path] = o
	c.mu.Unlock()
	return o, true
}

// cover paints a square cover at size dp, or a placeholder tile when the file
// has none. A placeholder rather than a gap: a list where only some rows have
// art would otherwise change its own row heights as the covers load.
func (a *App) cover(gtx C, path string, size unit.Dp) D {
	px := gtx.Dp(size)
	gtx.Constraints = layout.Exact(image.Pt(px, px))

	r := gtx.Dp(4)
	defer clip.RRect{Rect: image.Rectangle{Max: image.Pt(px, px)}, SE: r, SW: r, NE: r, NW: r}.Push(gtx.Ops).Pop()

	o, ok := a.art.op(path)
	if !ok {
		return a.coverPlaceholder(gtx, px)
	}
	return widget.Image{Src: o, Fit: widget.Cover, Position: layout.Center}.Layout(gtx)
}

// coverPlaceholder is the tile shown for music with no art: the panel colour and
// a dim record, which reads as "nothing here" rather than as a failed image.
//
// It is an icon rather than a "♪" character on purpose — the bundled Go fonts
// have no glyph for U+266A, so the text version drew nothing at all.
func (a *App) coverPlaceholder(gtx C, px int) D {
	paint.FillShape(gtx.Ops, colSel, clip.Rect{Max: image.Pt(px, px)}.Op())
	if iconNoCover == nil {
		return D{Size: image.Pt(px, px)}
	}
	return layout.Center.Layout(gtx, func(gtx C) D {
		gtx.Constraints = layout.Exact(image.Pt(px/2, px/2))
		return iconNoCover.Layout(gtx, colLine)
	})
}

// netCoverKey registers an album's network cover and returns the key that
// paints it: covers.op sees the prefix and fetches instead of reading a file.
// The registry maps the key back to its CoverRef for fetchNetCover; entries are
// a few strings and refs are stable identities, so it only ever grows within a
// session and is never rebuilt under the rows still on screen.
func (a *App) netCoverKey(ref library.CoverRef) string {
	key := netCoverPrefix + ref.Key()
	a.mu.Lock()
	if a.netCovers == nil {
		a.netCovers = map[string]library.CoverRef{}
	}
	a.netCovers[key] = ref
	a.mu.Unlock()
	return key
}

// fetchNetCover is covers.fetchNet: key back to ref, ref to bytes, through the
// library that produced it. Bounded — a cover is a nicety, and a relay that has
// to walk the mesh for it must not hold a paint slot open for minutes.
func (a *App) fetchNetCover(key string) ([]byte, error) {
	a.mu.Lock()
	ref, ok := a.netCovers[key]
	a.mu.Unlock()
	if !ok {
		return nil, errors.New("no album registered this cover key")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return a.lib.FetchCover(ctx, ref)
}

// albumHeaderCoverKey is the art for the tracks view's album header: a local
// file when any track here has one, else the album row's network cover. The
// order is the point — the file on disk is the truth about this machine, and
// the network ref is for the album that lives only on a server.
func (a *App) albumHeaderCoverKey(tracks []*library.Track) string {
	if p := albumCoverPath(tracks); p != "" {
		return p
	}
	a.mu.Lock()
	al := a.album
	a.mu.Unlock()
	if al != nil && !al.Cover.Zero() {
		return a.netCoverKey(al.Cover)
	}
	return ""
}

// albumCoverPath is the file an album's cover is read from: the first track on
// it that this machine actually holds.
//
// A remote-only album has none, and that is the honest answer — the cover is in
// bytes nobody here has downloaded. It appears the moment a track from the album
// plays, because then the download HAS landed and the player knows where.
func albumCoverPath(tracks []*library.Track) string {
	for _, t := range tracks {
		if p := t.LocalPath(); p != "" {
			return p
		}
	}
	return ""
}

// nowPlayingCoverPath is the art beside the transport: the file being decoded
// right now, which is the downloaded copy for a remote track and the original
// for a local one. Falling back to the queue item's own path covers the gap
// while a track is still loading.
func (a *App) nowPlayingCoverPath() string {
	if p := a.pl.CurrentPath(); p != "" {
		return p
	}
	if cur := a.pl.Current(); cur != nil {
		return cur.Path
	}
	return ""
}

// nowPlayingCoverKey is the key the player bar and the media widget paint: the
// playing FILE's own art when it has any, else the album's fetchable ref. The
// file wins because embedded art is the artist's answer for THIS track — but a
// sidecar-cover library's files embed nothing at all, and before this fallback
// every such track played faceless while its browse row sat there with the art.
func (a *App) nowPlayingCoverKey() string {
	path := a.nowPlayingCoverPath()
	if path != "" {
		if img, settled := a.art.cache.Get(path); !settled || img != nil {
			return path // has art, or the answer is still being read
		}
	}
	cur := a.pl.Current()
	if cur == nil || cur.Cover == "" {
		return path
	}
	ref, ok := library.ParseCoverKey(cur.Cover)
	if !ok {
		return path
	}
	return a.netCoverKey(ref)
}

// controls is the player as the desktop's media bus needs it: playback, plus
// the one thing playback does not know about — where the current cover is.
//
// It is an adapter rather than a method on the player because internal/player
// has no idea what a cover is, and giving it one to satisfy a bus would be the
// wrong direction entirely.
type controls struct {
	*player.Player
	art *covers
	// key resolves the current cover key — the App's nowPlayingCoverKey, so
	// the widget and the player bar can never disagree about whose art shows.
	key func() string
}

// ArtPath is a file the media widget can fetch, and it is the reason
// artwork.Cache can spill: a cover embedded in an audio file has no path of its
// own — and a fetched network cover never had one — while another process
// cannot read this one's memory either way.
func (c controls) ArtPath() string {
	if c.art == nil || c.key == nil {
		return ""
	}
	key := c.key()
	if key == "" {
		return ""
	}
	return c.art.cache.File(key)
}
