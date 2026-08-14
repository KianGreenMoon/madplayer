package blobcache

import (
	"context"
	"errors"
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

// watch wakes every reader when ctx ends, so a cancelled fetch does not leave
// one parked forever on a condition nobody will ever signal.
func (p *progress) watch(ctx context.Context) {
	go func() {
		<-ctx.Done()
		p.mu.Lock()
		if !p.done {
			p.done, p.err = true, errAbandoned
		}
		p.mu.Unlock()
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
		t.p.mu.Lock()
		for t.rd >= t.p.n && !t.p.done {
			t.p.cond.Wait()
		}
		avail, done, err := t.p.n-t.rd, t.p.done, t.p.err
		t.p.mu.Unlock()

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

	c.mu.Lock()
	if _, running := c.inflight[key]; running {
		// Somebody is already fetching this — the prefetch of the track that just
		// became current, usually. Wait for it rather than starting a second copy
		// of the same download.
		c.mu.Unlock()
		path, err := c.Get(ctx, key, ext, fetch)
		if err != nil {
			return nil, err
		}
		return os.Open(path)
	}

	// Derived from the CALLER's context, unlike Get's. Get detaches because a
	// fetch there is shared and outlives any one waiter; a stream has exactly one
	// reader, so its caller giving up is the end of it — and without that link a
	// decoder blocked on the first bytes of a track the person already skipped
	// would wait for a fetch nobody wants, forever.
	fctx, cancel := context.WithCancel(ctx)
	cl := &call{done: make(chan struct{}), cancel: cancel, waiters: 1}
	c.inflight[key] = cl
	c.mu.Unlock()

	prog := newProgress()
	prog.watch(fctx)

	part := c.path(key, ext) + ".part"
	if err := c.startFetch(fctx, key, ext, fetch, cl, prog); err != nil {
		return nil, err
	}

	// The reader opens the SAME file the fetch is writing. It is opened after the
	// writer created it, which is why startFetch does that part synchronously.
	f, err := os.Open(part)
	if err != nil {
		cl.cancel()
		return nil, err
	}
	return &streamed{tail: tail{f: f, p: prog}, cache: c, key: key, cl: cl}, nil
}

// streamed is a tail read plus the bookkeeping that says somebody is listening.
type streamed struct {
	tail
	cache *Cache
	key   string
	cl    *call
	once  sync.Once
}

// Close stops the fetch when this was the last reader of it. A person skipping
// through ten tracks should not be downloading ten.
func (s *streamed) Close() error {
	s.once.Do(func() {
		s.cache.mu.Lock()
		s.cl.waiters--
		abandoned := s.cl.waiters == 0
		s.cache.mu.Unlock()
		if abandoned {
			s.cl.cancel()
		}
	})
	return s.tail.Close()
}

// startFetch creates the part file and runs the fetch into it, metered.
//
// Creating the file happens on THIS goroutine so the caller can open it the
// moment this returns; a reader racing a writer for a file that does not exist
// yet is the one ordering bug this design could have.
func (c *Cache) startFetch(ctx context.Context, key, ext string, fetch Fetch, cl *call, prog *progress) error {
	final := c.path(key, ext)
	part := final + ".part"
	f, err := os.OpenFile(part, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		c.mu.Lock()
		delete(c.inflight, key)
		c.mu.Unlock()
		cl.cancel()
		close(cl.done)
		return err
	}

	go func() {
		defer func() {
			c.mu.Lock()
			delete(c.inflight, key)
			c.mu.Unlock()
			cl.cancel()
			close(cl.done)
		}()

		err := fetch(ctx, meter{w: f, p: prog})
		closeErr := f.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			// A half-written file must never be presented as a track: it would
			// decode into silence or noise and look like a corrupt original. The
			// reader still holds it open, so removing the name is enough — its
			// bytes go when that handle closes.
			_ = os.Remove(part)
			cl.err = err
			prog.finish(err)
			return
		}
		if err := os.Rename(part, final); err != nil {
			_ = os.Remove(part)
			cl.err = err
			prog.finish(err)
			return
		}
		cl.path = final
		prog.finish(nil)
		c.evict(filepath.Base(final))
	}()
	return nil
}
