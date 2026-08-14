package ui

import (
	"image"
	"sync"

	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"

	"golang.org/x/exp/shiny/materialdesign/icons"

	"daemonlord.ygg/madplayer/internal/artwork"
	"daemonlord.ygg/madplayer/internal/library"
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

	mu  sync.Mutex
	ops map[string]paint.ImageOp
}

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

	img, settled := c.cache.Get(path)
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

// iconNoCover is the record drawn on a coverless tile. A failure to build it is
// not worth reporting: the tile is then plain, which is the same message.
var iconNoCover = func() *widget.Icon {
	ic, err := widget.NewIcon(icons.AVAlbum)
	if err != nil {
		return nil
	}
	return ic
}()

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
