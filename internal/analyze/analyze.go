// Package analyze performs the ingest analysis madshare would otherwise shell
// out for: the tech columns ffprobe fills and the acoustic fingerprint fpcalc
// computes, both in this process.
//
// It is the join between three things that stay independent of each other:
// internal/probe reads containers, internal/chroma computes Chromaprint, and
// the player's own decoders turn a file into samples. None of them knows about
// madshare; the adapter that presents this as madshare's media.Tools lives in
// internal/backend, which is the one package allowed to import it.
//
// # Why the decoder is the interesting part
//
// The fingerprint has to agree with fpcalc's, because madshare compares
// fingerprints made on different machines. The algorithm is exact (see
// internal/chroma), so what is left is whether we hand it the same samples
// ffmpeg hands fpcalc — and for MP3 we do not, unless we make a point of it.
// See skipping below.
package analyze

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/gopxl/beep/v2"
	"github.com/gopxl/beep/v2/flac"
	"github.com/gopxl/beep/v2/mp3"
	"github.com/gopxl/beep/v2/vorbis"
	"github.com/gopxl/beep/v2/wav"

	"daemonlord.ygg/madplayer/internal/chroma"
	"daemonlord.ygg/madplayer/internal/probe"
)

// FingerprintSeconds is how much of a track is fingerprinted. It is fpcalc's
// own default, and matching it is not optional: a fingerprint of a whole track
// and a fingerprint of its first two minutes are different fingerprints, and
// these get compared across machines.
const FingerprintSeconds = 120

// Version identifies this implementation in the algo_version column.
//
// That column is not decoration. When two nodes disagree about the fingerprint
// of the same bytes, madshare files a claim report carrying both versions, and
// an operator reading it needs to see that one side was not fpcalc. Bump it if
// a change here moves any bit of the output.
const Version = "madplayer/1"

// decoders are the formats this build can turn into samples — the player's own
// list, and deliberately shorter than what the scanner indexes.
//
// A file this cannot decode gets no fingerprint and no duration, which the
// analysis pool already treats as a per-file failure to log and move past. It
// still gets its tech columns: those come from the container, and reading an
// M4A's header does not require being able to play it.
var decoders = map[string]func(io.ReadCloser) (beep.StreamSeekCloser, beep.Format, error){
	".mp3":  func(r io.ReadCloser) (beep.StreamSeekCloser, beep.Format, error) { return mp3.Decode(r) },
	".flac": func(r io.ReadCloser) (beep.StreamSeekCloser, beep.Format, error) { return flac.Decode(r) },
	".wav":  func(r io.ReadCloser) (beep.StreamSeekCloser, beep.Format, error) { return wav.Decode(r) },
	".ogg":  func(r io.ReadCloser) (beep.StreamSeekCloser, beep.Format, error) { return vorbis.Decode(r) },
	".oga":  func(r io.ReadCloser) (beep.StreamSeekCloser, beep.Format, error) { return vorbis.Decode(r) },
}

// Tech reads the file's technical facts. Header-only: no decoding.
func Tech(path string) (*probe.Info, error) {
	return probe.Inspect(path)
}

// Fingerprint computes the file's Chromaprint fingerprint, and returns it with
// the track's full duration — which is what fpcalc reports beside a fingerprint
// of the first two minutes, so it is what the column means.
func Fingerprint(ctx context.Context, path string) (raw []uint32, seconds float64, err error) {
	// A damaged file is a fact about the file, not a reason to end the program.
	//
	// The decoders trust their own headers: beep's WAV decoder computes a frame
	// size from the format chunk and indexes its read buffer by it, so a header
	// claiming a frame of zero bytes panics on the first sample rather than
	// reporting anything. This runs on a worker pool that has no recover of its
	// own, so that panic takes the whole process with it — a truncated download
	// or one damaged file in a scanned folder is enough. Caught here, at the
	// boundary where a bad file is already an ordinary outcome.
	defer func() {
		if r := recover(); r != nil {
			raw, seconds = nil, 0
			err = fmt.Errorf("analyze: %s could not be decoded (%v)", filepath.Base(path), r)
		}
	}()

	ext := strings.ToLower(filepath.Ext(path))
	decode, ok := decoders[ext]
	if !ok {
		return nil, 0, fmt.Errorf("analyze: no decoder for %s files in this build", ext)
	}

	// The container is read first, for two things at once: the duration to
	// report, and how much of the decoder's output to throw away.
	info, err := probe.Inspect(path)
	if err != nil {
		return nil, 0, err
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()

	stream, format, err := decode(f)
	if err != nil {
		return nil, 0, fmt.Errorf("analyze: decode %s: %w", filepath.Base(path), err)
	}
	defer stream.Close()
	if format.SampleRate <= 0 {
		return nil, 0, fmt.Errorf("analyze: %s: decoder reported no sample rate", filepath.Base(path))
	}

	src := stream.Stream
	if info.SkipSamples > 0 {
		src = skipping(src, info.SkipSamples)
	}
	raw, err = chroma.Compute(ctx, int(format.SampleRate), FingerprintSeconds, src)
	if err != nil {
		return nil, 0, err
	}
	return raw, info.DurationSeconds, nil
}

// skipping drops the first n samples of a stream.
//
// This is what makes an MP3 fingerprint comparable with fpcalc's, and it is not
// a correction to the decoder so much as one the decoder never applies. An
// encoder writes priming samples that are in the file and were never in the
// recording, and it writes its own header frame as a frame of silence; ffmpeg's
// demuxer drops both, the pure-Go decoder emits both, and the difference is a
// couple of thousand samples of lead-in. Small — and enough to shift every
// analysis window against the one fpcalc used, which was measured at a bit
// error rate of 0.06 against a threshold of 0.10.
//
// internal/probe works out how many; this only has to obey.
func skipping(stream func([][2]float64) (int, bool), n int) func([][2]float64) (int, bool) {
	left := n
	return func(out [][2]float64) (int, bool) {
		for left > 0 {
			// Drop into the caller's own buffer rather than allocating: it is
			// sized for streaming and is about to be overwritten anyway.
			chunk := out
			if left < len(chunk) {
				chunk = chunk[:left]
			}
			read, ok := stream(chunk)
			left -= read
			if !ok || read == 0 {
				return 0, false // the whole track was shorter than its own lead-in
			}
		}
		return stream(out)
	}
}
