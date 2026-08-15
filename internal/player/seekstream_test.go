package player

// The corners of seeking a still-arriving track: a target beyond what has
// downloaded, pause state across the source swap, and a restored position on a
// stream. The mechanism under test is player.seekStream / discardTo — the
// decoder's own Seek is never called (it panics on a non-seekable source).

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"daemonlord.ygg/madplayer/internal/queue"
)

// gated serves a file's first limit bytes, then blocks until the gate opens —
// a download whose watermark is stuck mid-track.
type gated struct {
	f     *os.File
	limit int64
	read  int64
	open  chan struct{}
}

func (g *gated) Read(b []byte) (int, error) {
	for {
		gateOpen := false
		select {
		case <-g.open:
			gateOpen = true
		default:
		}
		if !gateOpen {
			rest := g.limit - g.read
			if rest <= 0 {
				time.Sleep(time.Millisecond)
				continue
			}
			if int64(len(b)) > rest {
				b = b[:rest]
			}
		}
		n, err := g.f.Read(b)
		g.read += int64(n)
		if n > 0 {
			return n, nil
		}
		if err == io.EOF {
			if gateOpen {
				return 0, io.EOF
			}
			time.Sleep(time.Millisecond)
			continue
		}
		if err != nil {
			return 0, err
		}
	}
}

func (g *gated) Close() error { return g.f.Close() }

type gatedFetcher struct {
	path  string
	limit int64
	open  chan struct{}
}

func (s *gatedFetcher) Local(context.Context, *queue.Item) (string, error) {
	return s.path, nil
}

func (s *gatedFetcher) Stream(context.Context, *queue.Item) (io.ReadCloser, string, error) {
	f, err := os.Open(s.path)
	if err != nil {
		return nil, "", err
	}
	return &gated{f: f, limit: s.limit, open: s.open}, filepath.Ext(s.path), nil
}

// A scrub past the watermark waits for the bytes rather than refusing: the
// player says Loading, the old position keeps standing, and the moment the
// download reaches the target the seek lands.
func TestASeekBeyondTheWatermarkWaitsForTheBytes(t *testing.T) {
	dir := t.TempDir()
	path := writeWAV(t, dir, "a.wav", 4)

	// writeWAV is mono 16-bit at 44100 Hz: ~88 KB per second plus the header.
	// One second is downloaded; the seek aims at 2.5.
	fetch := &gatedFetcher{path: path, limit: 44 + 88200, open: make(chan struct{})}
	p, err := New(&fakeSink{})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	p.SetFetcher(fetch)

	p.SetQueue([]*queue.Item{{URL: "https://elsewhere/a.wav", Duration: 4}}, 0)
	waitPlaying(t, p)

	p.Seek(2.5)
	waitFor(t, "the seek to report itself as loading", p.Loading)
	if elapsed, _ := p.Position(); elapsed > 1.2 {
		t.Fatalf("position jumped to %.2f with only ~1s of bytes on disk", elapsed)
	}

	close(fetch.open) // the download catches up
	waitFor(t, "the seek to land once the bytes arrived", func() bool {
		elapsed, _ := p.Position()
		return elapsed >= 2.4 && elapsed <= 2.7
	})
	if p.Loading() {
		t.Error("still claiming to load after the seek landed")
	}
}

// Scrubbing a paused stream stays paused: the swap carries the ctrl state
// across, so a seek is never an accidental play.
func TestASeekOnAPausedStreamStaysPaused(t *testing.T) {
	dir := t.TempDir()
	path := writeWAV(t, dir, "a.wav", 3)

	p, err := New(&fakeSink{})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	p.SetFetcher(&streamFetcher{path: path, stop: make(chan struct{})})

	p.SetQueue([]*queue.Item{{URL: "https://elsewhere/a.wav"}}, 0)
	waitPlaying(t, p)
	p.Pause()

	p.Seek(1.5)
	waitFor(t, "the seek to land", func() bool {
		elapsed, _ := p.Position()
		return elapsed >= 1.4 && elapsed <= 1.7
	})
	if !p.Paused() {
		t.Error("a seek on a paused stream resumed playback")
	}
}

// A restored position works on a streaming track too: load decodes its way to
// the saved offset instead of native-seeking, same consumption rules as a
// local file (docs/ui/player-and-queue.md — resume once, drop on navigation).
func TestARestoredPositionResumesOnAStream(t *testing.T) {
	dir := t.TempDir()
	path := writeWAV(t, dir, "a.wav", 4)

	p, err := New(&fakeSink{})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	p.SetFetcher(&streamFetcher{path: path, stop: make(chan struct{})})

	p.Restore([]*queue.Item{{URL: "https://elsewhere/a.wav"}}, nil, 0, false, queue.RepeatOff)
	p.ResumeAt(2)
	p.Toggle()
	waitPlaying(t, p)

	waitFor(t, "the resume to land", func() bool {
		elapsed, _ := p.Position()
		return elapsed >= 1.9 && elapsed <= 2.3
	})
}
