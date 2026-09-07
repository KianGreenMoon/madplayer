// Package icon draws madplayer's icon.
//
// One drawing for every place the program is pictured: the Android launcher,
// the freedesktop entry that puts it in a menu and gives the desktop's media
// widget something to draw, and the tray item that stands for the node while
// the window is closed. Generated rather than committed so the repo carries
// no binary blob — and so the icon is reviewable as the code that draws it.
package icon

import (
	"image"
	"image/color"
)

// Draw renders the icon at size×size pixels: a play triangle on a dark
// ground, by half-plane test so the edges stay clean at any size.
func Draw(size int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	bg := color.NRGBA{R: 0x14, G: 0x16, B: 0x1a, A: 0xff}
	fg := color.NRGBA{R: 0x4c, G: 0x8d, B: 0xff, A: 0xff}

	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			img.Set(x, y, bg)
		}
	}

	// Apex right-centre, base on the left.
	left := size * 30 / 100
	right := size * 74 / 100
	top := size * 24 / 100
	bot := size * 76 / 100
	midY := (top + bot) / 2
	if midY == top {
		// Too small for a triangle; the ground alone is still the right colour.
		return img
	}
	for y := top; y < bot; y++ {
		// Width shrinks linearly with distance from the vertical centre.
		d := y - midY
		if d < 0 {
			d = -d
		}
		span := (right - left) * (midY - top - d) / (midY - top)
		for x := left; x < left+span; x++ {
			img.Set(x, y, fg)
		}
	}
	return img
}
