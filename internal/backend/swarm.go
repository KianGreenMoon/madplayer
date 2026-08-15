package backend

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"daemonlord.ygg/madshare/app"
	"daemonlord.ygg/madshare/federation"
)

// FetchBlob downloads one blob from the holders named and opens it for reading.
//
// It BLOCKS until the transfer is complete and verified. That used to be
// justified by "the decoders want the whole file on disk, so a partial-read
// surface would be a feature with no caller" — both halves of which are now
// false. The decoders take a reader (internal/blobcache/stream.go), and the
// caller exists: see StreamBlob below, which is what playback uses. This is kept
// for the one job that genuinely wants every byte before it starts — keeping a
// track on this device, where a half-copied file would be worse than a wait.
//
// The bytes land in madshare's OWN download cache — hash-named, no extension —
// and that is the point rather than an inconvenience. It is the only directory
// this node seeds from (federation's seedableBlob) and the only one Holdings
// advertises, so fetching over the swarm is also what makes this device useful
// to the household. The caller copies them into its playback cache, which is
// where a name a decoder can read gets attached; the two copies are the two
// caches docs/ui/madplayer.md §"A remote track is a download" already describes,
// each swept by its own enforcer under the same ceiling.
//
// size may be 0 when the caller does not know it; the manifest is the authority
// either way.
func (b *Backend) FetchBlob(ctx context.Context, hash string, size int64, holders []string) (io.ReadCloser, error) {
	if b.net == nil {
		return nil, app.ErrNoMesh
	}
	start := time.Now()
	t, err := b.net.Fetch(ctx, hash, size, holders)
	if err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		// The transfer is governed by the same context, so abandoning it here is
		// enough — it winds itself down rather than running on for a track nobody
		// is waiting for any more.
		//
		// It is logged FIRST, and that matters more than the success case does.
		// A swarm fetch that expires is the one this side cannot explain: the
		// error says "context deadline exceeded" and nothing else, while the
		// transfer knows which holders it asked, how many chunks landed, whether
		// it was refused, retried or left idle. Discarding exactly that on the
		// path where the mesh looks broken is how "the swarm doesn't work" became
		// unanswerable for a week, twice.
		b.logTransfer(t, time.Since(start))
		return nil, ctx.Err()
	case <-t.Done():
	}
	b.logTransfer(t, time.Since(start))
	if err := t.Err(); err != nil {
		return nil, err
	}
	return t.Open()
}

// StreamBlob is FetchBlob without the wait: a reader over the swarm's own
// partial file, handed over as soon as the first byte lands.
//
// firstByte bounds only the START. That is the whole change in shape: the old
// budget bounded the ENTIRE transfer, because playback could not begin until the
// last byte arrived, so a slow swarm meant silence and the relay had to be given
// a chance while there was still time. Now that a track plays from the bytes as
// they land, a transfer that takes nineteen seconds is not a nineteen-second
// wait — it is a track that started at second one. What still deserves a
// deadline is a swarm that never answers at all, and that is what this bounds.
//
// Measured on the real mesh before this was built: this device's home server
// served a 19.3 MiB track in 18.9s, against a 20s whole-transfer budget. The
// swarm was losing that race by a second and falling back to the relay every
// time, having already spent the twenty seconds.
//
// The reader is NOT an io.Seeker, deliberately — same rule as the blobcache's:
// it is what lets a decoder start on a fraction of a file.
func (b *Backend) StreamBlob(ctx context.Context, hash string, size int64, holders []string, firstByte time.Duration) (io.ReadCloser, error) {
	if b.net == nil {
		return nil, app.ErrNoMesh
	}
	start := time.Now()
	// The TRANSFER gets the caller's context, not the first-byte one: the
	// deadline is on the wait, not on the download it is waiting for.
	t, err := b.net.Fetch(ctx, hash, size, holders)
	if err != nil {
		return nil, err
	}

	openCtx, cancel := context.WithTimeout(ctx, firstByte)
	defer cancel()
	if err := t.WaitFor(openCtx, 0); err != nil {
		// Nothing arrived in time, or the transfer died. Either way this is a
		// decline with no bytes written, so the relay can still take over.
		b.logTransfer(t, time.Since(start))
		if terr := t.Err(); terr != nil {
			return nil, terr
		}
		return nil, err
	}

	f, err := t.Open()
	if err != nil {
		b.logTransfer(t, time.Since(start))
		return nil, err
	}
	return &swarmReader{b: b, t: t, f: f, ctx: ctx, start: start}, nil
}

// swarmReader reads a transfer's file as it fills.
//
// Available bounds each read to what is contiguously readable from here, which
// is what keeps it from reading past the watermark into a hole the swarm has not
// filled yet; WaitFor parks until there is more. Between them they are the same
// pair madshare's own streaming relay uses, so these are bytes it already
// considers safe to serve — each chunk is sha256-verified as it lands.
type swarmReader struct {
	b     *Backend
	t     federation.Transfer
	f     *os.File
	ctx   context.Context
	start time.Time
	off   int64
	once  sync.Once
}

func (r *swarmReader) Read(p []byte) (int, error) {
	for {
		avail := r.t.Available(r.off)
		if avail <= 0 {
			// WaitFor returns io.EOF at or beyond the end, and the transfer's own
			// error when it failed — so both endings arrive here rather than
			// having to be inferred from a short read.
			if err := r.t.WaitFor(r.ctx, r.off); err != nil {
				if terr := r.t.Err(); terr != nil {
					return 0, terr
				}
				return 0, err
			}
			continue
		}
		if int64(len(p)) > avail {
			p = p[:avail]
		}
		n, err := r.f.ReadAt(p, r.off)
		r.off += int64(n)
		if n > 0 {
			return n, nil
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return 0, err
		}
	}
}

func (r *swarmReader) Close() error {
	r.once.Do(func() { r.b.logTransfer(r.t, time.Since(r.start)) })
	return r.f.Close()
}

// logTransfer says how the fetch went, per holder.
//
// One line per remote track is a price worth paying, because without it "the
// mesh is slow" is unanswerable from this side: the transfer knows which holder
// sent what, how often it retried and how often a connection went idle, and all
// of that was being discarded at the facade. The first real measurement of this
// client's swarm (2026-08-09) had to be reconstructed from wall-clock times and
// arithmetic on constants, which is not a thing to do twice.
func (b *Backend) logTransfer(t federation.Transfer, took time.Duration) {
	s := t.Stats()
	b.log.Printf("madplayer: swarm %s… %s mode=%s %d/%d chunks in %s (retries=%d failovers=%d stalls=%d corrupt=%d)",
		shortHash(s.Hash), human(s.Size), s.Mode, s.ChunksDone, s.Chunks,
		took.Round(time.Millisecond), s.Retries, s.Failovers, s.Stalls, s.Corrupt)
	for _, p := range s.Providers {
		// LastError is the field that answers "why": a holder that refuses because
		// it is too old to know what a listener node is says something completely
		// different from one that simply never answered, and both look like
		// "0 bytes" without it.
		note := ""
		if p.Dropped {
			note = " — retired"
		}
		if p.LastError != "" {
			note += ": " + p.LastError
		}
		b.log.Printf("madplayer:   holder %s… %s in %d chunk(s), %d failure(s)%s",
			shortHash(p.PublicKey), human(p.Bytes), p.Chunks, p.Failures, note)
	}
}

func shortHash(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

func human(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KiB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}
