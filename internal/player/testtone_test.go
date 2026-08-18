package player

// The tone exists to answer one question — is the fault above the sink or
// below it — so what it has to be is exactly what it claims: the device's
// rate, nothing decoded, and an end.

import (
	"math"
	"testing"

	"github.com/gopxl/beep/v2"

	"daemonlord.ygg/madplayer/internal/queue"
)

func TestTheTestToneIsGeneratedAtTheDeviceRate(t *testing.T) {
	// A device that answers 48000 to a 44100 request, as a phone does.
	sink := &fakeSink{rate: 48000}
	p, err := New(sink)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	p.PlayTestTone()

	sink.mu.Lock()
	s := sink.s
	sink.mu.Unlock()
	if s == nil {
		t.Fatal("the tone did not reach the sink")
	}

	buf := make([][2]float64, 4800) // a tenth of a second at the DEVICE's rate
	n, ok := s.Stream(buf)
	if n != len(buf) || !ok {
		t.Fatalf("the tone delivered %d samples (ok=%v), want %d", n, ok, len(buf))
	}
	// One hundred cycles of 1 kHz fit in a tenth of a second at 48 kHz, and
	// only at 48 kHz: at 44.1 the same buffer would hold 108. Counting zero
	// crossings is how the rate is checked without trusting the generator's
	// own arithmetic.
	crossings := 0
	for i := 1; i < len(buf); i++ {
		if buf[i-1][0] <= 0 && buf[i][0] > 0 {
			crossings++
		}
	}
	if crossings != 100 {
		t.Errorf("the tone completed %d cycles in 4800 device samples, want 100 — it is not being generated at the device's rate", crossings)
	}
	var peak float64
	for _, s := range buf {
		peak = math.Max(peak, math.Abs(s[0]))
		if s[0] != s[1] {
			t.Fatalf("the tone is not identical in both channels at %v", s)
		}
	}
	if math.Abs(peak-toneAmp) > 0.01 {
		t.Errorf("tone peak %.3f, want %.2f", peak, toneAmp)
	}
}

// A diagnostic that never ends is a bug of its own: the mixer must drop it.
func TestTheTestToneEnds(t *testing.T) {
	rate := beep.SampleRate(48000)
	tn := &tone{rate: rate, n: int(rate) * toneSeconds}
	buf := make([][2]float64, 512)
	total := 0
	for {
		n, ok := tn.Stream(buf)
		total += n
		if !ok {
			break
		}
	}
	if want := int(rate) * toneSeconds; total != want {
		t.Errorf("the tone ran for %d samples, want %d", total, want)
	}
}

// Pressing it while a track plays must stop the track, or the two mix and
// the tone tests nothing.
func TestTheTestToneStopsPlayback(t *testing.T) {
	dir := t.TempDir()
	sink := &fakeSink{}
	p, err := New(sink)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	p.SetQueue([]*queue.Item{{Path: writeWAV(t, dir, "a.wav", 5)}}, 0)
	waitPlaying(t, p)

	p.PlayTestTone()
	if p.Playing() {
		t.Error("the track is still playing under the tone")
	}
}
