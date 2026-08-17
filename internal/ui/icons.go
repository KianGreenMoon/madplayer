package ui

import (
	"image"
	"image/color"

	"gioui.org/f32"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"

	"golang.org/x/exp/shiny/materialdesign/icons"
)

// The icons this program draws.
//
// They come from the Material Design set in golang.org/x/exp/shiny, which Gio
// already pulls in — so this is a set of vector glyphs rather than a font
// dependency or a folder of PNGs. Building one can fail (the data is parsed),
// and a nil icon simply draws nothing: an icon is never the only way to
// understand a control, so a missing one costs decoration and not function.
var (
	iconPlayNext = mustIcon(icons.AVPlaylistPlay)
	iconAddQueue = mustIcon(icons.AVPlaylistAdd)
	iconUp       = mustIcon(icons.NavigationArrowUpward)
	iconDown     = mustIcon(icons.NavigationArrowDownward)
	iconRemove   = mustIcon(icons.ContentClear)
	// iconKeep is "keep this on the device": a download, which is literally what
	// it does — the bytes come off the network onto this disk.
	iconKeep = mustIcon(icons.FileFileDownload)
	// iconKept is the same row once the file is here.
	iconKept = mustIcon(icons.ActionDone)
	// iconNoCover is the record drawn on a tile whose music has no art.
	iconNoCover = mustIcon(icons.AVAlbum)

	// The transport, in the web UI's order: shuffle · prev · play · next ·
	// repeat. These are the one place an icon needs no worded twin — they are
	// the same glyphs every player has drawn for fifty years — and the keyboard
	// list in Settings still spells the actions out.
	iconShuffle   = mustIcon(icons.AVShuffle)
	iconPrev      = mustIcon(icons.AVSkipPrevious)
	iconPlay      = mustIcon(icons.AVPlayArrow)
	iconPause     = mustIcon(icons.AVPause)
	iconNext      = mustIcon(icons.AVSkipNext)
	iconRepeat    = mustIcon(icons.AVRepeat)
	iconRepeatOne = mustIcon(icons.AVRepeatOne)
	// iconLoading stands on the play button while a download is what is
	// actually happening — the bar's subtitle names the server it comes from.
	iconLoading = mustIcon(icons.ActionHourglassEmpty)

	// The Settings panel's actions.
	iconAddFolder = mustIcon(icons.FileCreateNewFolder)
	iconRescan    = mustIcon(icons.NavigationRefresh)
	iconDelete    = mustIcon(icons.ActionDelete)
	iconSave      = mustIcon(icons.ContentSave)
	// The clipboard pair beside every settings text box (clipboard.go). These
	// two are the exception to the rule above them: their worded twin is a
	// keyboard shortcut, which is exactly what a phone does not have — so the
	// glyphs carry the meaning alone, and they are the two the Material set
	// draws most literally.
	iconCopy  = mustIcon(icons.ContentContentCopy)
	iconPaste = mustIcon(icons.ContentContentPaste)
)

func mustIcon(data []byte) *widget.Icon {
	ic, err := widget.NewIcon(data)
	if err != nil {
		return nil
	}
	return ic
}

// iconButton is a small square button carrying one icon.
//
// It is used where a word would not fit — on a track row, beside a queue entry —
// and never for an action that has no worded twin somewhere the person has
// already seen. The album header spells out the same three actions, which is
// what teaches these.
func (a *App) iconButton(gtx C, click *widget.Clickable, ic *widget.Icon, size unit.Dp) D {
	px := gtx.Dp(size)
	return click.Layout(gtx, func(gtx C) D {
		gtx.Constraints = layout.Exact(image.Pt(px, px))
		if click.Hovered() {
			r := gtx.Dp(4)
			rect := clip.RRect{Rect: image.Rectangle{Max: image.Pt(px, px)}, SE: r, SW: r, NE: r, NW: r}
			paint.FillShape(gtx.Ops, colSel, rect.Op(gtx.Ops))
		}
		if ic == nil {
			return D{Size: image.Pt(px, px)}
		}
		return layout.Center.Layout(gtx, func(gtx C) D {
			gtx.Constraints = layout.Exact(image.Pt(px*3/5, px*3/5))
			col := colDim
			if click.Hovered() {
				col = colFg
			}
			return ic.Layout(gtx, col)
		})
	})
}

// ctrlButton is one transport control, the web UI's .ctrl-btn: a quiet glyph
// that gains a circle on hover and holds the accent while its mode is on —
// which is how shuffle and repeat say they are engaged without a word.
func (a *App) ctrlButton(gtx C, click *widget.Clickable, ic *widget.Icon, size unit.Dp, active bool) D {
	var draw func(C, color.NRGBA) D
	if ic != nil {
		draw = ic.Layout
	}
	return a.ctrlFrame(gtx, click, size, active, draw)
}

// ctrlFrame is the shell every transport control shares — the hover circle and
// the state colouring — with the glyph drawing handed in, so a control whose
// glyph the Material set does not carry (the queue button) wears the same
// frame as its siblings.
func (a *App) ctrlFrame(gtx C, click *widget.Clickable, size unit.Dp, active bool, draw func(C, color.NRGBA) D) D {
	px := gtx.Dp(size)
	return click.Layout(gtx, func(gtx C) D {
		gtx.Constraints = layout.Exact(image.Pt(px, px))
		if click.Hovered() {
			circle := clip.Ellipse{Max: image.Pt(px, px)}
			paint.FillShape(gtx.Ops, colSel, circle.Op(gtx.Ops))
		}
		if draw == nil {
			return D{Size: image.Pt(px, px)}
		}
		return layout.Center.Layout(gtx, func(gtx C) D {
			gtx.Constraints = layout.Exact(image.Pt(px*3/5, px*3/5))
			col := colDim
			switch {
			case active:
				col = colAccent
			case click.Hovered():
				col = colFg
			}
			return draw(gtx, col)
		})
	})
}

// queueGlyph is the web UI's #btnQueue icon, transcribed from its SVG path
// (`M3 10h11v2H3zm0-4h11v2H3zm0 8h7v2H3zm13-1v8l6-4z`, 24×24 viewBox). The
// Material set Gio ships only has the older cut of playlist-play — lines
// nearly the full box wide with a short triangle — which sits beside the web
// UI's like a squashed stranger, so this one is drawn by hand.
func queueGlyph(gtx C, col color.NRGBA) D {
	sz := gtx.Constraints.Min
	s := float32(sz.X) / 24
	var p clip.Path
	p.Begin(gtx.Ops)
	line := func(x0, y0, x1, y1 float32) {
		p.MoveTo(f32.Pt(x0*s, y0*s))
		p.LineTo(f32.Pt(x1*s, y0*s))
		p.LineTo(f32.Pt(x1*s, y1*s))
		p.LineTo(f32.Pt(x0*s, y1*s))
		p.Close()
	}
	line(3, 6, 14, 8)
	line(3, 10, 14, 12)
	line(3, 14, 10, 16)
	p.MoveTo(f32.Pt(16*s, 13*s))
	p.LineTo(f32.Pt(16*s, 21*s))
	p.LineTo(f32.Pt(22*s, 17*s))
	p.Close()
	paint.FillShape(gtx.Ops, col, clip.Outline{Path: p.End()}.Op())
	return D{Size: sz}
}

// primaryButton is the play/pause control, the web UI's .ctrl-btn.primary: an
// accent-filled circle, a shade darker under the pointer, with a white glyph.
// One control on the bar gets to be loud, and it is the one that answers what
// a music player is for.
func (a *App) primaryButton(gtx C, click *widget.Clickable, ic *widget.Icon, size unit.Dp) D {
	px := gtx.Dp(size)
	return click.Layout(gtx, func(gtx C) D {
		gtx.Constraints = layout.Exact(image.Pt(px, px))
		bg := colAccent
		if click.Hovered() {
			bg = colAccentDim
		}
		circle := clip.Ellipse{Max: image.Pt(px, px)}
		paint.FillShape(gtx.Ops, bg, circle.Op(gtx.Ops))
		if ic == nil {
			return D{Size: image.Pt(px, px)}
		}
		return layout.Center.Layout(gtx, func(gtx C) D {
			gtx.Constraints = layout.Exact(image.Pt(px*3/5, px*3/5))
			return ic.Layout(gtx, colOnAccent)
		})
	})
}

// actionButton is a Settings action as an icon: the filled square that stands
// where a worded button stood. It keeps a visible background at rest — unlike
// the row icons, it is not competing with forty siblings for quiet — and takes
// the accent while its action runs (a rescan in flight).
func (a *App) actionButton(gtx C, click *widget.Clickable, ic *widget.Icon, active bool) D {
	px := gtx.Dp(34)
	return click.Layout(gtx, func(gtx C) D {
		gtx.Constraints = layout.Exact(image.Pt(px, px))
		bg := colSel
		if click.Hovered() {
			bg = colLine
		}
		if active {
			bg = colAccent
		}
		r := gtx.Dp(8)
		rect := clip.RRect{Rect: image.Rectangle{Max: image.Pt(px, px)}, SE: r, SW: r, NE: r, NW: r}
		paint.FillShape(gtx.Ops, bg, rect.Op(gtx.Ops))
		if ic == nil {
			return D{Size: image.Pt(px, px)}
		}
		return layout.Center.Layout(gtx, func(gtx C) D {
			gtx.Constraints = layout.Exact(image.Pt(px*3/5, px*3/5))
			col := colFg
			if active {
				col = colOnAccent
			}
			return ic.Layout(gtx, col)
		})
	})
}
