package blobcache

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

// Reading a blob while it is still arriving.
//
// The package comment used to say there was "no useful sense in which this
// client streams", on the grounds that go-mp3 walks every frame header before
// reporting a length and beep's flac needs an io.ReadSeeker. Both halves are
// true only for a SEEKABLE reader, which is the one thing a growing file is not.
// Measured on real files (2026-08-15):
//
//	6.6 MB mp3   decoder ready in  60 ms having read 0.0% of the file
//	37 MB flac   decoder ready in 107 ms having read 0.2%, first audio at 156 ms
//
// go-mp3 skips the length walk entirely when its source is not an io.Seeker
// (decode.go: `if _, ok := d.source.reader.(io.Seeker); !ok { return nil }`) and
// beep's flac picks `flac.New` over `flac.NewSeek` for the same reason. So the
// whole file was never the requirement — handing a decoder an *os.File was.
//
// The cost is real and is paid on purpose: a stream cannot seek, and an mp3
// cannot report its own length. FLAC still can, since STREAMINFO carries the
// sample count.

// tail reads a file that is still being written.
//
// At the end of what has been written it WAITS rather than returning io.EOF,
// because to a decoder those are the same event and one of them is a lie. It
// returns io.EOF only when the writer says it is finished, and the writer's
// error when it failed.
type tail struct {
	f  *os.File
	p  *progress
	rd int64
	// ctx is THIS reader's caller, not the fetch's. They are different lifetimes
	// now that several readers can share one fetch: a reader giving up must stop
	// that reader, and the fetch carries on for whoever else is listening.
	ctx context.Context
}

// progress is the writer's side: how much has been written, and whether it is
// still going. One per fetch, shared with every reader of it.
type progress struct {
	mu   sync.Mutex
	cond *sync.Cond
	n    int64
	done bool
	err  error
}

func newProgress() *progress {
	p := &progress{}
	p.cond = sync.NewCond(&p.mu)
	return p
}

// wrote advances the watermark and wakes the readers.
func (p *progress) wrote(n int64) {
	p.mu.Lock()
	p.n += n
	p.mu.Unlock()
	p.cond.Broadcast()
}

// finish closes the stream, successfully or not. Waking every reader is the
// point: a reader parked at the tail of a fetch that just failed would otherwise
// wait for bytes that are never coming.
func (p *progress) finish(err error) {
	p.mu.Lock()
	p.done, p.err = true, err
	p.mu.Unlock()
	p.cond.Broadcast()
}

// stop is what a cancelled context does to the readers. It is not an error the
// caller wrote — it is this process deciding it no longer wants the track.
var errAbandoned = errors.New("the download was stopped")

// watch wakes every reader when ctx ends, so nobody is left parked on a
// condition that will never be signalled. A context that can never be cancelled
// is not watched at all, or the goroutine would outlive the process's interest
// in it.
//
// It only WAKES them; whether the fetch is finished is the fetch's own business
// (finish). One reader giving up must not tell the others the download ended —
// they may still be reading it, and it may still be running for them.
func (p *progress) watch(ctx context.Context) {
	if ctx.Done() == nil {
		return
	}
	go func() {
		<-ctx.Done()
		p.cond.Broadcast()
	}()
}

// meter counts what passes through to the file and tells the readers.
type meter struct {
	w io.Writer
	p *progress
}

func (m meter) Write(b []byte) (int, error) {
	n, err := m.w.Write(b)
	if n > 0 {
		m.p.wrote(int64(n))
	}
	return n, err
}

// Read blocks at the tail rather than reporting the end of a file that has not
// ended.
//
// Deliberately NOT an io.Seeker, and that is the whole mechanism: the decoders
// choose their streaming path by asking whether their source seeks, so a growing
// file has to answer no.
func (t *tail) Read(b []byte) (int, error) {
	for {
		if err := t.ctx.Err(); err != nil {
			return 0, err
		}
		t.p.mu.Lock()
		for t.rd >= t.p.n && !t.p.done && t.ctx.Err() == nil {
			t.p.cond.Wait()
		}
		avail, done, err := t.p.n-t.rd, t.p.done, t.p.err
		t.p.mu.Unlock()

		if err := t.ctx.Err(); err != nil {
			return 0, err
		}
		if avail <= 0 {
			if err != nil {
				return 0, err
			}
			if done {
				return 0, io.EOF
			}
			continue
		}
		if int64(len(b)) > avail {
			b = b[:avail]
		}
		n, rerr := t.f.Read(b)
		t.rd += int64(n)
		if n > 0 {
			// A short read at the tail is ordinary; report the bytes and let the
			// next call decide whether to wait.
			return n, nil
		}
		if rerr != nil && !errors.Is(rerr, io.EOF) {
			return 0, rerr
		}
	}
}

func (t *tail) Close() error { return t.f.Close() }

// Stream opens a blob for playback, starting the fetch if it is not already on
// disk.
//
// A blob already cached comes back as an *os.File — a Seeker, so the decoder
// takes its seeking path and everything behaves exactly as it did before. One
// still arriving comes back as a reader that blocks at the tail and cannot seek,
// which is what lets a decoder start on 0.2% of a file.
//
// The returned reader is the caller's to close, and closing it is what says
// nobody is waiting on this fetch any more.
func (c *Cache) Stream(ctx context.Context, key, ext string, fetch Fetch) (io.ReadCloser, error) {
	if path, ok := c.Lookup(key, ext); ok {
		return os.Open(path)
	}

	cl, err := c.begin(ctx, key, ext, fetch)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(cl.part)
	if err != nil {
		c.drop(cl)
		return nil, err
	}
	// This reader's OWN cancellation has to wake it out of the shared condition;
	// the fetch's does not speak for it, and another reader's certainly does not.
	cl.prog.watch(ctx)
	return &streamed{tail: tail{f: f, p: cl.prog, ctx: ctx}, cache: c, cl: cl}, nil
}

// begin starts the fetch for key, or JOINS the one already running, counting
// the caller as a waiter either way. Every caller must drop when finished.
//
// One path for both Get and Stream is the point rather than tidiness. Get is
// what a prefetch uses (it wants the whole file) and Stream is what playback
// uses, so on an album the prefetch of track 2 is running when the player asks
// for track 2 — and a fetch nobody can tail means the player waits out the whole
// download. Measured before this: 403ms of a 403ms download, for every track
// after the first.
func (c *Cache) begin(ctx context.Context, key, ext string, fetch Fetch) (*call, error) {
	c.mu.Lock()
	if cl, running := c.inflight[key]; running {
		cl.waiters++
		c.mu.Unlock()
		return cl, nil
	}

	// DETACHED from any one caller and reference-counted: the fetch is shared, so
	// the first caller giving up must not cancel a download the second is
	// listening to. What stops it is the last waiter leaving.
	//
	// The part name carries a sequence number — see Cache.seq for why a fetch
	// must never share a part file with the dying fetch it replaces.
	fctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	prog := newProgress()
	c.seq++
	cl := &call{
		key:     key,
		done:    make(chan struct{}),
		cancel:  cancel,
		waiters: 1,
		prog:    prog,
		part:    fmt.Sprintf("%s.%d.part", c.path(key, ext), c.seq),
	}
	c.inflight[key] = cl
	c.mu.Unlock()

	prog.watch(fctx)
	if err := c.startFetch(fctx, key, ext, fetch, cl, prog); err != nil {
		return nil, err
	}
	return cl, nil
}

// drop releases one caller's interest, stopping the fetch when that was the
// last — and only then removing what a failed fetch left behind, since a reader
// still holding on has an error to be told about first.
//
// Deciding "last" and unregistering the call are ONE critical section. Deciding
// first and cancelling later left a window in which a new caller found the call
// still registered, joined it, and inherited the cancellation — which the
// player reads as "the user skipped", so the click silently did nothing
// (reproduced 2026-08-15). A caller arriving after this now starts fresh.
func (c *Cache) drop(cl *call) {
	c.mu.Lock()
	cl.waiters--
	last := cl.waiters == 0
	failed := cl.err != nil
	if last && c.inflight[cl.key] == cl {
		delete(c.inflight, cl.key)
	}
	c.mu.Unlock()
	if !last {
		return
	}
	cl.cancel()
	if failed {
		// A half-written file must never be presented as a track: it would
		// decode into silence or noise and look like a corrupt original. A fetch
		// still dying at this point has err unset yet — it removes its own part
		// when it finishes and finds no waiters left (startFetch).
		_ = os.Remove(cl.part)
	}
}

// streamed is a tail read plus the bookkeeping that says somebody is listening.
type streamed struct {
	tail
	cache *Cache
	cl    *call
	once  sync.Once
}

// Close stops the fetch when this was the last reader of it. A person skipping
// through ten tracks should not be downloading ten.
func (s *streamed) Close() error {
	s.once.Do(func() { s.cache.drop(s.cl) })
	return s.tail.Close()
}

// startFetch creates the part file and runs the fetch into it, metered.
//
// The file is created on THIS goroutine, before the fetch can run, so a caller
// can open it for reading the moment begin returns. What a failed fetch leaves
// behind is NOT removed here: a reader that is still attached has an error to be
// told about first, and the removal happens when the last waiter leaves (drop).
// Removing it here was a race the reader lost, and lost confusingly — reporting
// "no such file" instead of why the fetch failed.
func (c *Cache) startFetch(ctx context.Context, key, ext string, fetch Fetch, cl *call, prog *progress) error {
	// The delete is guarded by identity: drop unregisters the call the moment
	// the last waiter leaves, and a FRESH call for the same key may already be
	// in the map by the time this dying one gets here. An unguarded delete
	// would evict the newcomer.
	unregister := func() {
		c.mu.Lock()
		if c.inflight[key] == cl {
			delete(c.inflight, key)
		}
		c.mu.Unlock()
	}

	final := c.path(key, ext)
	f, err := os.OpenFile(cl.part, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		unregister()
		cl.cancel()
		close(cl.done)
		return err
	}

	go func() {
		defer func() {
			unregister()
			cl.cancel()
			close(cl.done)
		}()

		err := fetch(ctx, meter{w: f, p: prog})
		closeErr := f.Close()
		if err == nil {
			err = closeErr
		}
		if err == nil {
			err = os.Rename(cl.part, final)
		}
		if err != nil {
			// The half-written file stays while a waiter is attached: the readers
			// need the error, not a vanished file, and the last of them removes it
			// on the way out (drop). When nobody is left — the ordinary way a
			// fetch dies, since the last reader leaving is what cancels it — the
			// removal is this goroutine's job, or the part sat on disk until the
			// next launch's reaper.
			c.mu.Lock()
			cl.err = err
			orphaned := cl.waiters == 0
			c.mu.Unlock()
			if orphaned {
				_ = os.Remove(cl.part)
			}
			prog.finish(err)
			return
		}
		cl.path = final
		prog.finish(nil)
		c.evict(filepath.Base(final))
	}()
	return nil
}
