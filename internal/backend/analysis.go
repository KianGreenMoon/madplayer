package backend

import (
	"context"

	"daemonlord.ygg/madshare/media"

	"daemonlord.ygg/madplayer/internal/analyze"
)

// The ingest analysis, performed here rather than by two child processes.
//
// madshare's default runs ffprobe and fpcalc off PATH, which is right for a
// server and impossible on a phone: nothing to install, nothing allowed to
// execute, and no process of our own to re-exec. Without it a device gets no
// tech columns, no fingerprints, no duplicate detection — and no mesh, because
// a node that cannot verify what it downloads must not redistribute it.
//
// This is the adapter and nothing else. The work is in internal/analyze, which
// knows nothing about madshare; this file is the only place the two meet,
// keeping to the rule that this package is the only one importing the server.

// tools implements madshare's media.Tools over this program's own decoders.
type tools struct{}

// Available: both, always. A format this build cannot decode fails per file,
// which the analysis pool logs and moves past — the difference between "this
// installation cannot fingerprint" and "this file could not be fingerprinted"
// is exactly the difference this answer is about.
func (tools) Available() (probe, fingerprint bool) { return true, true }

// ProbeTech fills the columns ffprobe would fill, out of the container's own
// header.
func (tools) ProbeTech(_ context.Context, path string) (*media.TechInfo, error) {
	info, err := analyze.Tech(path)
	if err != nil {
		return nil, err
	}
	return &media.TechInfo{
		DurationSeconds: info.DurationSeconds,
		Bitrate:         info.Bitrate,
		SampleRate:      info.SampleRate,
		Channels:        info.Channels,
		BitDepth:        info.BitDepth,
		Codec:           info.Codec,
	}, nil
}

// ComputeFingerprint computes what fpcalc would compute, and agrees with it:
// bit-identical on undecoded audio, and within a bit error rate of 0.0006 on
// real MP3s against a matching threshold of 0.10 (internal/analyze's tests
// measure it against the real binary).
func (tools) ComputeFingerprint(ctx context.Context, path string) (*media.Fingerprint, error) {
	raw, seconds, err := analyze.Fingerprint(ctx, path)
	if err != nil {
		return nil, err
	}
	return &media.Fingerprint{
		Algo: "chromaprint",
		// Not a chromaprint version, on purpose. When two nodes disagree about
		// the fingerprint of the same bytes madshare files a claim report
		// carrying both versions, and whoever reads it needs to see that one
		// side was not fpcalc.
		AlgoVersion: analyze.Version,
		// The whole track's length, not the fingerprinted part's: that is what
		// fpcalc reports beside a fingerprint of the first two minutes, so it is
		// what the column already means everywhere else.
		Duration: seconds,
		Raw:      raw,
	}, nil
}
