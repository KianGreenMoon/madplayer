package icon

import (
	"image/color"
	"testing"
)

func TestDrawIsATriangleOnAGround(t *testing.T) {
	for _, size := range []int{16, 22, 32, 512} {
		img := Draw(size)
		if img.Bounds().Dx() != size || img.Bounds().Dy() != size {
			t.Fatalf("size %d: drew %v", size, img.Bounds())
		}
		bg := color.NRGBA{R: 0x14, G: 0x16, B: 0x1a, A: 0xff}
		if c := img.NRGBAAt(0, 0); c != bg {
			t.Fatalf("size %d: corner is %v, want the ground", size, c)
		}
		// The apex row's leftmost third is triangle at every size that can hold one.
		if size >= 16 {
			c := img.NRGBAAt(size*32/100, size/2)
			if c == bg {
				t.Fatalf("size %d: no triangle at the base of the apex row", size)
			}
		}
	}
}
