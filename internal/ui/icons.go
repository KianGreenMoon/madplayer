package ui

import (
	"image"

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
	// iconNoCover is the record drawn on a tile whose music has no art.
	iconNoCover = mustIcon(icons.AVAlbum)
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
