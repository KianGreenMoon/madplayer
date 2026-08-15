package chroma_test

import (
	"context"
	"encoding/json"
	"math"
	"math/bits"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/gopxl/beep/v2"
	"github.com/gopxl/beep/v2/wav"

	"daemonlord.ygg/madplayer/internal/chroma"
)

// The only test that matters here: does this agree with fpcalc?
//
// A fingerprint is worth nothing on its own. It is compared with fingerprints
// computed elsewhere — a phone's against the server's — at madshare's 10% bit
// error threshold, so what has to be measured is the DISAGREEMENT with the real
// binary, on real audio, and how much of that budget it spends.
//
// fpcalc is not a build dependency of this program (its absence is the whole
// reason this package exists), so these skip without it.

// maxBitErrorRate mirrors database.maxBitErrorRate in madshare: the threshold
// two fingerprints must come within to be judged the same recording.
const maxBitErrorRate = 0.10

// budget is what this implementation is allowed to spend of that threshold. Far
// under it, because the threshold's real job is absorbing a re-encode, not
// absorbing us: a listener comparing a phone's rip with a server's transcode
// needs the room. A regression that pushes past this is a bug even though
// matching would still work.
const budget = 0.02

func TestAgreesWithFpcalcOnSynthesisedAudio(t *testing.T) {
	requireFpcalc(t)
	path := filepath.Join(t.TempDir(), "tone.wav")
	writeWAV(t, path, synthesise(44100, 30))
	compare(t, path, budget)
}

// compare fingerprints one file both ways and reports how far apart they are.
func compare(t *testing.T, path string, allow float64) {
	t.Helper()
	theirs := fpcalcRaw(t, path)
	ours := ourRaw(t, path)

	if len(ours) == 0 {
		t.Fatal("produced no sub-fingerprints")
	}
	// A length difference is its own finding: it means the two disagree about
	// how much audio there is, which no amount of bit-level agreement fixes.
	if d := len(ours) - len(theirs); d < -1 || d > 1 {
		t.Errorf("length %d, fpcalc %d (%+d): the two read different amounts of audio",
			len(ours), len(theirs), d)
	}

	ber := bitErrorRate(ours, theirs)
	t.Logf("%s: %d frames, BER %.4f (fpcalc %d frames)", filepath.Base(path), len(ours), ber, len(theirs))
	if ber > allow {
		t.Errorf("BER %.4f exceeds the %.2f budget (madshare matches below %.2f)", ber, allow, maxBitErrorRate)
	}
}

// bitErrorRate is media.BitErrorRate: differing bits over the common prefix.
func bitErrorRate(a, b []uint32) float64 {
	n := min(len(a), len(b))
	if n == 0 {
		return 1
	}
	var diff int
	for i := 0; i < n; i++ {
		diff += bits.OnesCount32(a[i] ^ b[i])
	}
	return float64(diff) / float64(n*32)
}

func requireFpcalc(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("fpcalc"); err != nil {
		t.Skip("fpcalc not installed; nothing to compare against")
	}
}

// fpcalcRaw runs the real thing with the arguments madshare runs it with.
func fpcalcRaw(t *testing.T, path string) []uint32 {
	t.Helper()
	out, err := exec.Command("fpcalc", "-json", "-raw", path).Output()
	if err != nil {
		t.Fatalf("fpcalc %s: %v", path, err)
	}
	var parsed struct {
		Duration    float64 `json:"duration"`
		Fingerprint []int64 `json:"fingerprint"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("fpcalc output: %v", err)
	}
	raw := make([]uint32, len(parsed.Fingerprint))
	for i, v := range parsed.Fingerprint {
		raw[i] = uint32(v)
	}
	return raw
}

// ourRaw fingerprints the file in process. WAV, because this test is about the
// algorithm and a WAV is the one container both sides read identically — a
// codec's own rounding belongs to internal/analyze's test, not to this one.
func ourRaw(t *testing.T, path string) []uint32 {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	s, format, err := wav.Decode(f)
	if err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	defer s.Close()

	// 120 seconds: fpcalc's own default, and therefore the only length that
	// compares.
	raw, err := chroma.Compute(context.Background(), int(format.SampleRate), 120, s.Stream)
	if err != nil {
		t.Fatalf("fingerprint %s: %v", path, err)
	}
	return raw
}

// synthesise builds audio with energy spread over the chroma bands and moving
// between them, so the classifiers have something to disagree about. A single
// steady tone would agree trivially.
func synthesise(rate int, seconds float64) [][2]float64 {
	n := int(float64(rate) * seconds)
	out := make([][2]float64, n)
	// A chord walking up the scale, plus a beating partial and a little noise.
	semitones := []float64{0, 4, 7, 11}
	rnd := uint32(12345)
	for i := range out {
		t := float64(i) / float64(rate)
		step := math.Floor(t * 2) // a new chord twice a second
		var v float64
		for k, s := range semitones {
			f := 110 * math.Pow(2, (s+math.Mod(step, 12))/12) * float64(k+1)
			v += math.Sin(2*math.Pi*f*t) / float64(len(semitones)+1)
		}
		rnd = rnd*1664525 + 1013904223
		v += (float64(rnd>>8)/float64(1<<24) - 0.5) * 0.02
		out[i] = [2]float64{v * 0.7, v * 0.7}
	}
	return out
}

// writeWAV writes 16-bit stereo PCM, the format both sides read identically.
func writeWAV(t *testing.T, path string, samples [][2]float64) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	format := beep.Format{SampleRate: 44100, NumChannels: 2, Precision: 2}
	if err := wav.Encode(f, &sliceStreamer{samples: samples}, format); err != nil {
		t.Fatalf("encode wav: %v", err)
	}
}

type sliceStreamer struct {
	samples [][2]float64
	pos     int
}

func (s *sliceStreamer) Stream(buf [][2]float64) (int, bool) {
	if s.pos >= len(s.samples) {
		return 0, false
	}
	n := copy(buf, s.samples[s.pos:])
	s.pos += n
	return n, true
}

func (s *sliceStreamer) Err() error { return nil }
