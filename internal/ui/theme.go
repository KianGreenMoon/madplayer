// Package ui is madplayer's Gio front end.
//
// It is an offline music player first: it scans folders, indexes what it finds
// and plays it, with no server, no account and no network. Reaching a madshare
// server is a feature layered on top of that, never a precondition — see
// docs/ui/madplayer.md.
//
// The browse behaviour follows docs/ui/library-page.md and
// docs/ui/artists-and-performers.md, and the queue follows
// docs/ui/player-and-queue.md. Those are cross-client contracts: where this
// disagrees with the web UI, the two stop being the same product.
package ui

import (
	"image/color"

	"gioui.org/widget/material"
)

func rgb(v uint32) color.NRGBA {
	return color.NRGBA{R: uint8(v >> 16), G: uint8(v >> 8), B: uint8(v), A: 0xff}
}

var (
	colBg     = rgb(0x14161a)
	colBar    = rgb(0x1b1e24)
	colLine   = rgb(0x2a2f38)
	colFg     = rgb(0xe8eaed)
	colDim    = rgb(0x9aa2ae)
	colAccent = rgb(0x4c8dff)
	colSel    = rgb(0x212936)
	colWarn   = rgb(0xff8a65)
)

func newTheme() *material.Theme {
	th := material.NewTheme()
	th.Palette = material.Palette{Bg: colBg, Fg: colFg, ContrastBg: colAccent, ContrastFg: rgb(0xffffff)}
	return th
}
