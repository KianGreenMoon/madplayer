package player

// A signal this program makes itself, for the question no counter in it can
// answer.
//
// Everything from the decoder to the sink now reports on itself: the sink
// times each pull and models the pool oto keeps, the ring logs its dry
// spells, the player names each track's rate. On 2026-08-18 all of them
// stayed silent through two minutes of an album the owner could hear
// crackling — worst pull 6 ms against a 250 ms runway, no starved feed, no
// dry ring, GC quiet, reads exactly at realtime. The same day, the samples
// those stages produce were checked against a plain resample of the same
// decode and matched to the last bit, and the resampler was measured never
// to leave ±1. So the audio handed to the device is right, and the fault is
// somewhere no counter of ours reaches: oto's C++ fifo, oboe's OpenSL
// stream, AudioFlinger, the amplifier, the speaker.
//
// A tone splits that. It is generated at the DEVICE's rate — no file, no
// decoder, no ring, no resampler, nothing that can be behind realtime — and
// handed straight to the sink, so what plays exercises only the sink's
// encoder and everything below it. Any interruption in that chain is
// unmistakable on a steady sine, where music merely sounds "off". Crackle on
// the tone means the fault is below the sink and no change above it can help;
// a clean tone means the transport is sound and the fault is in the audio
// itself or in what the speaker does with loud, dense music.
//
// It is bounded rather than endless: a diagnostic left running by accident
// would be worse than no diagnostic.

import (
	"log"
	"math"

	"github.com/gopxl/beep/v2"
)

const (
	// toneFreq is 1 kHz, the frequency every audio measurement is quoted at:
	// mid-band, no speaker resonance, and a glitch in it is a click nobody
	// can miss.
	toneFreq = 1000

	// toneAmp is -6 dBFS. Loud enough to hear over a room, quiet enough that
	// nothing downstream is being asked to clip — the point is to test the
	// path, not the amplifier's limits.
	toneAmp = 0.5

	// toneSeconds is how long it plays. Long enough for a crackle that comes
	// every few seconds to show itself, short enough that it always ends.
	toneSeconds = 20
)

// tone is a sine at the sink's rate.
type tone struct {
	rate beep.SampleRate
	i, n int
}

func (t *tone) Stream(samples [][2]float64) (int, bool) {
	if t.i >= t.n {
		return 0, false
	}
	k := min(len(samples), t.n-t.i)
	for j := 0; j < k; j++ {
		v := toneAmp * math.Sin(2*math.Pi*toneFreq*float64(t.i+j)/float64(t.rate))
		samples[j][0], samples[j][1] = v, v
	}
	t.i += k
	return k, true
}

func (t *tone) Err() error { return nil }

// PlayTestTone stops whatever is playing and feeds the sink a tone this
// program generates at the device's own rate. It returns the line to show
// the person who pressed it, which is also the line the log gets: a
// diagnostic whose result is a memory of what somebody heard needs the log
// to say what was playing at the time.
func (p *Player) PlayTestTone() string {
	p.stop()

	p.mu.Lock()
	rate := p.rate
	p.mu.Unlock()

	log.Printf("player: test tone — %d Hz sine at %.0f%% for %ds, generated at the device's %d Hz (no decoder, no ring, no resampler)",
		toneFreq, toneAmp*100, toneSeconds, rate)
	p.sink.Play(&tone{rate: rate, n: int(rate) * toneSeconds})
	return "Playing a 1 kHz tone for 20 s, generated at the device's rate — no decoder, no resampler. If THIS crackles, the fault is below the audio sink."
}
