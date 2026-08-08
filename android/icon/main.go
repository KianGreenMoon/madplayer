// Command icon writes the launcher icon the Android packagers require.
//
// It is generated rather than committed so the repo carries no binary blob for
// a throwaway spike — and so the icon is reviewable as the code that draws it.
package main

import (
	"image"
	"image/color"
	"image/png"
	"log"
	"os"
)

const size = 512

func main() {
	out := "icon.png"
	if len(os.Args) > 1 {
		out = os.Args[1]
	}

	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	bg := color.NRGBA{R: 0x14, G: 0x16, B: 0x1a, A: 0xff}
	fg := color.NRGBA{R: 0x4c, G: 0x8d, B: 0xff, A: 0xff}

	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			img.Set(x, y, bg)
		}
	}

	// A play triangle, drawn by half-plane test so the edges stay clean at any
	// size: apex right-centre, base on the left.
	const (
		left  = size * 30 / 100
		right = size * 74 / 100
		top   = size * 24 / 100
		bot   = size * 76 / 100
	)
	midY := (top + bot) / 2
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

	f, err := os.Create(out)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		log.Fatal(err)
	}
}
