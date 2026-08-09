package backend

import (
	"context"
	"fmt"
	"io"
	"time"

	"daemonlord.ygg/madshare/app"
	"daemonlord.ygg/madshare/federation"
)

// FetchBlob downloads one blob from the holders named and opens it for reading.
//
// It BLOCKS until the transfer is complete and verified, which looks like it
// throws away the streaming half of a swarm and does not: the pure-Go decoders
// want the whole file on disk (docs/ui/madplayer.md §"A remote track is a
// download, not a stream"), so there is nothing this client could do with a
// half-arrived one. A partial-read surface would be a feature with no caller.
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
		return nil, ctx.Err()
	case <-t.Done():
	}
	b.logTransfer(t, time.Since(start))
	if err := t.Err(); err != nil {
		return nil, err
	}
	return t.Open()
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
