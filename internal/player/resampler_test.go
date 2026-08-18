package player

// The adapter between the decoder and internal/resample. What it has to get
// right is stereo (two independent filters, never crossed), delivery in
// whatever sizes the fill asks for, and where the stream ends.

import (
	"math"
	"testing"

	"github.com/gopxl/beep/v2"
)

// tone streams a stereo signal whose two channels differ, so a wrapper that
// crossed or shared them would be caught.
type tone struct {
	rate   float64
	left   float64 // frequency
	right  float64
	i, n   int
	short  int // return this many samples once, mid-stream, without ending
	shortI int
}

func (t *tone) Stream(p [][2]float64) (int, bool) {
	if t.i >= t.n {
		return 0, false
	}
	k := min(len(p), t.n-t.i)
	if t.short > 0 && t.i >= t.shortI {
		k, t.short = min(k, t.short), 0
	}
	for j := 0; j < k; j++ {
		x := float64(t.i + j)
		p[j][0] = 0.9 * math.Sin(2*math.Pi*t.left*x/t.rate)
		p[j][1] = 0.5 * math.Sin(2*math.Pi*t.right*x/t.rate)
	}
	t.i += k
	return k, true
}

func (t *tone) Err() error { return nil }

func drain(rs beep.Streamer, chunk int) [][2]float64 {
	var got [][2]float64
	buf := make([][2]float64, chunk)
	for {
		n, ok := rs.Stream(buf)
		got = append(got, buf[:n]...)
		if !ok {
			return got
		}
	}
}

// Each channel must come out as its own tone, at the right frequency and the
// right amplitude — the test that would fail if the two filters shared state.
func TestTheResamplerKeepsTheChannelsApart(t *testing.T) {
	src := &tone{rate: 44100, left: 1000, right: 4000, n: 44100}
	got := drain(newResampler(src, 44100, 48000), 512)
	if len(got) < 47000 || len(got) > 48100 {
		t.Fatalf("one second of 44100 became %d frames at 48000", len(got))
	}
	for _, c := range []struct {
		name string
		i    int
		f    float64
		amp  float64
	}{{"left", 0, 1000, 0.9}, {"right", 1, 4000, 0.5}} {
		var sigSq, errSq float64
		for n := 2000; n < len(got)-2000; n++ {
			ideal := c.amp * math.Sin(2*math.Pi*c.f*float64(n)/48000)
			d := got[n][c.i] - ideal
			sigSq += ideal * ideal
			errSq += d * d
		}
		if snr := 10 * math.Log10(sigSq/errSq); snr < 80 {
			t.Errorf("%s channel: error only %.1f dB below its own tone", c.name, snr)
		}
	}
}

// The fill asks for whatever space the ring has, down to a single sample.
func TestTheResamplerServesAnySize(t *testing.T) {
	whole := drain(newResampler(&tone{rate: 44100, left: 1000, right: 4000, n: 20000}, 44100, 48000), 512)

	rs := newResampler(&tone{rate: 44100, left: 1000, right: 4000, n: 20000}, 44100, 48000)
	var got [][2]float64
	buf := make([][2]float64, 1000)
	for i := 0; ; i++ {
		n, ok := rs.Stream(buf[:1+i%997])
		got = append(got, buf[:n]...)
		if !ok {
			break
		}
	}
	if len(got) != len(whole) {
		t.Fatalf("ragged reads gave %d frames, 512-frame reads gave %d", len(got), len(whole))
	}
	for i := range whole {
		if got[i] != whole[i] {
			t.Fatalf("frame %d differs between read sizes: %v vs %v", i, got[i], whole[i])
		}
	}
}

// beep's Resampler treats the first under-full read as end-of-data and never
// asks the decoder again. A decoder reading a file that is still arriving
// returns short reads without being finished, so that behaviour would end the
// track early — this one ends only when the decoder says so.
func TestAShortReadIsNotTheEnd(t *testing.T) {
	src := &tone{rate: 44100, left: 1000, right: 4000, n: 20000, short: 7, shortI: 4096}
	got := drain(newResampler(src, 44100, 48000), 512)
	if len(got) < 21000 {
		t.Errorf("a short read mid-stream ended the track at %d frames, want the whole %d", len(got), 20000*48000/44100)
	}
}

// The tail inside the filter is real audio and is owed: the stream ends after
// it, not before it.
func TestTheTailIsDelivered(t *testing.T) {
	const n = 4410
	src := &tone{rate: 44100, left: 1000, right: 4000, n: n}
	got := drain(newResampler(src, 44100, 48000), 512)
	if want := n * 48000 / 44100; len(got) < want-2 {
		t.Errorf("delivered %d frames, want %d — the filter's tail was dropped", len(got), want)
	}
}

// What the converter costs, against the one it replaced. The claim in this
// file's comment is that the Kaiser sinc is both better AND cheaper, and the
// second half is the one that decides whether the fill keeps up on a phone's
// little core — so it is measured rather than reasoned from operation counts,
// which overstate it by more than tenfold.
type replay struct {
	s [][2]float64
	i int
}

func (r *replay) Stream(p [][2]float64) (int, bool) {
	if r.i >= len(r.s) {
		return 0, false
	}
	n := copy(p, r.s[r.i:])
	r.i += n
	return n, true
}

func (r *replay) Err() error { return nil }

func benchSource() [][2]float64 {
	s := make([][2]float64, 441000) // ten seconds at 44.1 kHz
	for i := range s {
		s[i][0] = 0.9 * math.Sin(2*math.Pi*1000*float64(i)/44100)
		s[i][1] = 0.5 * math.Sin(2*math.Pi*4000*float64(i)/44100)
	}
	return s
}

func benchResample(b *testing.B, wrap func(beep.Streamer) beep.Streamer) {
	src := benchSource()
	buf := make([][2]float64, 512)
	b.ResetTimer()
	for b.Loop() {
		rs := wrap(&replay{s: src})
		for {
			if _, ok := rs.Stream(buf); !ok {
				break
			}
		}
	}
}

func BenchmarkKaiserSinc(b *testing.B) {
	benchResample(b, func(s beep.Streamer) beep.Streamer { return newResampler(s, 44100, 48000) })
}

func BenchmarkBeepLagrange8(b *testing.B) {
	benchResample(b, func(s beep.Streamer) beep.Streamer { return beep.Resample(8, 44100, 48000, s) })
}
