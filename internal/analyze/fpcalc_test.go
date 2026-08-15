package analyze_test

import (
	"context"
	"encoding/json"
	"math/bits"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"daemonlord.ygg/madplayer/internal/analyze"
)

// Does the whole path — this program's decoders, the lead-in it drops, and its
// own Chromaprint — agree with fpcalc on real files?
//
// internal/chroma proves the ALGORITHM against fpcalc on synthesised PCM, where
// there is no decoder to disagree. This proves the rest of it, which is the
// part that can only be measured on files somebody actually has: an encoder's
// priming samples, a decoder's rounding, a container's idea of where the audio
// starts.
//
// fpcalc is not a build dependency (its absence is why this package exists), so
// this skips without it. Point MADPLAYER_FPCALC_CORPUS at a music directory.

// maxBitErrorRate mirrors database.maxBitErrorRate in madshare: how close two
// fingerprints must be to be judged the same recording. Two nodes that disagree
// by more than this about the same bytes do not merely fail to match — one files
// a contradiction report about the other.
const maxBitErrorRate = 0.10

// budget is what this implementation may spend of that. A fifth, because the
// threshold exists to absorb a re-encode, not to absorb us.
const budget = 0.02

func TestFingerprintsAgreeWithFpcalc(t *testing.T) {
	if _, err := exec.LookPath("fpcalc"); err != nil {
		t.Skip("fpcalc not installed; nothing to compare against")
	}
	dir := os.Getenv("MADPLAYER_FPCALC_CORPUS")
	if dir == "" {
		t.Skip("set MADPLAYER_FPCALC_CORPUS to a directory of audio files")
	}

	var found int
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !decodable(path) {
			return nil
		}
		found++
		t.Run(filepath.Base(path), func(t *testing.T) {
			theirs := fpcalcRaw(t, path)
			ours, seconds, err := analyze.Fingerprint(context.Background(), path)
			if err != nil {
				t.Fatalf("fingerprint: %v", err)
			}
			if len(ours) == 0 {
				t.Fatal("no sub-fingerprints")
			}
			// A length difference means the two read different amounts of audio,
			// which no amount of bit-level agreement makes up for.
			if diff := len(ours) - len(theirs); diff < -1 || diff > 1 {
				t.Errorf("%d frames, fpcalc %d (%+d)", len(ours), len(theirs), diff)
			}
			ber := bitErrorRate(ours, theirs)
			t.Logf("%.1fs, %d frames, BER %.5f", seconds, len(ours), ber)
			if ber > budget {
				t.Errorf("BER %.5f over the %.2f budget (madshare matches below %.2f)",
					ber, budget, maxBitErrorRate)
			}
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	if found == 0 {
		t.Fatalf("no decodable audio under %s", dir)
	}
}

func decodable(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mp3", ".flac", ".wav", ".ogg", ".oga":
		return true
	}
	return false
}

// fpcalcRaw runs the real binary with the arguments madshare runs it with.
func fpcalcRaw(t *testing.T, path string) []uint32 {
	t.Helper()
	out, err := exec.Command("fpcalc", "-json", "-raw", path).Output()
	if err != nil {
		t.Fatalf("fpcalc: %v", err)
	}
	var parsed struct {
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

// bitErrorRate is media.BitErrorRate: differing bits over the common prefix.
func bitErrorRate(a, b []uint32) float64 {
	n := min(len(a), len(b))
	if n == 0 {
		return 1
	}
	var diff int
	for i := range n {
		diff += bits.OnesCount32(a[i] ^ b[i])
	}
	return float64(diff) / float64(n*32)
}
