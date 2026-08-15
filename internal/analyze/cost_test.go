package analyze_test

import (
	"context"
	"os"
	"testing"

	"daemonlord.ygg/madplayer/internal/analyze"
)

// What analysis costs, because a scan pays it once per file while somebody
// waits for their library — and on a phone, out of a battery.
//
// Measured on a 202-second MP3 (aarch64 laptop, 2026-08-15):
//
//	Fingerprint   5.7 s      fpcalc, for comparison: 0.18 s
//	Tech         40 µs       ffprobe, for comparison: ~30 ms of process start
//
// The gap is the DECODER, not the fingerprinter. A CPU profile puts ~75% in
// go-mp3's subband synthesis and IMDCT, ~14% in beep converting each sample to
// a float (it calls math.Exp2 per sample), and ~11% in this package's resampler
// and FFT together. A pure-Go MP3 decoder is roughly an order of magnitude off
// ffmpeg's hand-written SIMD, and closing that is a different project from this
// one.
//
// It is a background pool of one job per file, so the number that matters is
// not this one but whether a first scan finishes while somebody is still
// interested. Worth re-measuring on a phone before deciding it is fine.
func BenchmarkFingerprint(b *testing.B) {
	path := corpusFile(b)
	for b.Loop() {
		if _, _, err := analyze.Fingerprint(context.Background(), path); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkTech(b *testing.B) {
	path := corpusFile(b)
	for b.Loop() {
		if _, err := analyze.Tech(path); err != nil {
			b.Fatal(err)
		}
	}
}

func corpusFile(b *testing.B) string {
	b.Helper()
	path := os.Getenv("MADPLAYER_ANALYZE_FILE")
	if path == "" {
		b.Skip("set MADPLAYER_ANALYZE_FILE to an audio file to measure against")
	}
	return path
}
