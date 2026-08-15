package chroma

import "math"

// The half of Chromaprint that turns a chroma image into 32 bits per frame.
//
// Everything in this file is a TRAINED CONSTANT or a consequence of one. The
// classifier table below is Chromaprint's CHROMAPRINT_ALGORITHM_TEST2 — the
// default algorithm, the one fpcalc emits and the one AcoustID indexes. The
// numbers are not tunable and not ours: change one and this stops producing
// fingerprints anybody else can compare against.

// numBands is the chroma image's width: twelve pitch classes.
const numBands = 12

// filter shapes are the six area comparisons a classifier can make over the
// image, each reading a rectangle w frames wide and h bands tall at (x, y).
// Every one ends in a log ratio of two sums, which is what makes them
// insensitive to overall loudness.
type filter struct {
	kind      int
	y, height int
	width     int
	quantizer [3]float64
}

// classifiersTest2 is Chromaprint's kClassifiersTest2, in order. Their outputs
// are concatenated two bits at a time, most significant first, into one
// sub-fingerprint word.
var classifiersTest2 = [16]filter{
	{0, 4, 3, 15, [3]float64{1.98215, 2.35817, 2.63523}},
	{4, 4, 6, 15, [3]float64{-1.03809, -0.651211, -0.282167}},
	{1, 0, 4, 16, [3]float64{-0.298702, 0.119262, 0.558497}},
	{3, 8, 2, 12, [3]float64{-0.105439, 0.0153946, 0.135898}},
	{3, 4, 4, 8, [3]float64{-0.142891, 0.0258736, 0.200632}},
	{4, 0, 3, 5, [3]float64{-0.826319, -0.590612, -0.368214}},
	{1, 2, 2, 9, [3]float64{-0.557409, -0.233035, 0.0534525}},
	{2, 7, 3, 4, [3]float64{-0.0646826, 0.00620476, 0.0784847}},
	{2, 6, 2, 16, [3]float64{-0.192387, -0.029699, 0.215855}},
	{2, 1, 3, 2, [3]float64{-0.0397818, -0.00568076, 0.0292026}},
	{5, 10, 1, 15, [3]float64{-0.53823, -0.369934, -0.190235}},
	{3, 6, 2, 10, [3]float64{-0.124877, 0.0296483, 0.139239}},
	{2, 1, 1, 14, [3]float64{-0.101475, 0.0225617, 0.231971}},
	{3, 5, 6, 4, [3]float64{-0.0799915, -0.00729616, 0.063262}},
	{1, 9, 2, 12, [3]float64{-0.272556, 0.019424, 0.302559}},
	{3, 4, 2, 14, [3]float64{-0.164292, -0.0321188, 0.0846339}},
}

// maxFilterWidth is the widest classifier, and so how many frames must have
// arrived before the first sub-fingerprint can be computed.
const maxFilterWidth = 16

// grayCode maps a quantizer's four levels onto two bits so that adjacent levels
// differ in one bit. It is why a small change in the audio costs one bit rather
// than two, and therefore why comparing fingerprints by bit error works at all.
var grayCode = [4]uint32{0, 1, 3, 2}

// quantize places a filter's value into one of four trained levels.
func (f *filter) quantize(v float64) int {
	switch {
	case v < f.quantizer[1]:
		if v < f.quantizer[0] {
			return 0
		}
		return 1
	case v < f.quantizer[2]:
		return 2
	default:
		return 3
	}
}

// subtractLog compares two area sums as a log ratio. The +1 keeps it finite for
// the silent case, where both sums are zero.
func subtractLog(a, b float64) float64 {
	return math.Log((1 + a) / (1 + b))
}

// apply reads the filter's two regions at frame offset x and compares them.
// Every bound is exclusive at the far edge, matching Chromaprint's integral
// image, whose Area(r1,c1,r2,c2) excludes r2 and c2.
func (f *filter) apply(img *integralImage, x int) float64 {
	y, w, h := f.y, f.width, f.height
	switch f.kind {
	case 0: // the whole rectangle against nothing
		return subtractLog(img.area(x, y, x+w, y+h), 0)
	case 1: // upper bands against lower
		h2 := h / 2
		return subtractLog(img.area(x, y+h2, x+w, y+h), img.area(x, y, x+w, y+h2))
	case 2: // later frames against earlier
		w2 := w / 2
		return subtractLog(img.area(x+w2, y, x+w, y+h), img.area(x, y, x+w2, y+h))
	case 3: // one diagonal pair of quadrants against the other
		w2, h2 := w/2, h/2
		a := img.area(x, y+h2, x+w2, y+h) + img.area(x+w2, y, x+w, y+h2)
		b := img.area(x, y, x+w2, y+h2) + img.area(x+w2, y+h2, x+w, y+h)
		return subtractLog(a, b)
	case 4: // the middle third of the bands against the outer two
		h3 := h / 3
		a := img.area(x, y+h3, x+w, y+2*h3)
		b := img.area(x, y, x+w, y+h3) + img.area(x, y+2*h3, x+w, y+h)
		return subtractLog(a, b)
	case 5: // the middle third of the frames against the outer two
		w3 := w / 3
		a := img.area(x+w3, y, x+2*w3, y+h)
		b := img.area(x, y, x+w3, y+h) + img.area(x+2*w3, y, x+w, y+h)
		return subtractLog(a, b)
	}
	return 0
}

// integralImage accumulates chroma rows so any rectangle's sum is four lookups.
//
// It is ROLLING: only the last maxRows rows are kept, because no classifier
// reaches further back and a whole track's image is memory a phone should not
// spend. Row i lives at i%maxRows, which is safe precisely because nothing ever
// asks for an older one.
type integralImage struct {
	maxRows int
	cols    int
	data    []float64
	rows    int
}

func newIntegralImage(maxRows, cols int) *integralImage {
	// One row of slack: area() reads the row BEFORE a rectangle's first, so the
	// oldest reachable row is the one preceding the oldest classifier row.
	maxRows++
	return &integralImage{maxRows: maxRows, cols: cols, data: make([]float64, maxRows*cols)}
}

func (img *integralImage) row(i int) []float64 {
	i %= img.maxRows
	return img.data[i*img.cols : (i+1)*img.cols]
}

// addRow appends one chroma frame, storing it as a running sum along the row
// plus the running sum of every row before it.
func (img *integralImage) addRow(features []float64) {
	cur := img.row(img.rows)
	var sum float64
	for i, v := range features {
		sum += v
		cur[i] = sum
	}
	if img.rows > 0 {
		prev := img.row(img.rows - 1)
		for i := range cur {
			cur[i] += prev[i]
		}
	}
	img.rows++
}

// area sums the rectangle [r1,r2) x [c1,c2).
func (img *integralImage) area(r1, c1, r2, c2 int) float64 {
	if r1 == r2 || c1 == c2 {
		return 0
	}
	if r1 == 0 {
		row := img.row(r2 - 1)
		if c1 == 0 {
			return row[c2-1]
		}
		return row[c2-1] - row[c1-1]
	}
	row1, row2 := img.row(r1-1), img.row(r2-1)
	if c1 == 0 {
		return row2[c2-1] - row1[c2-1]
	}
	return row2[c2-1] - row1[c2-1] - row2[c1-1] + row1[c1-1]
}
