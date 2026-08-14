package player

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"daemonlord.ygg/madplayer/internal/queue"
)

// Playing a track while it is still arriving.
//
// The claim is that the decoders never needed the whole file — they needed a
// reader, and only take their whole-file path when handed one that seeks.
// Measured on real music before this was built: a 6.6 MB mp3 decoder was ready
// after 60 ms having read 0.0% of it, and a 37 MB flac after 107 ms at 0.2%.

// growing is a reader over a file that is still being written, with the one
// property that makes streaming work: it is NOT an io.Seeker.
type growing struct {
	f    *os.File
	stop chan struct{}
}

func (g *growing) Read(b []byte) (int, error) {
	for {
		n, err := g.f.Read(b)
		if n > 0 {
			return n, nil
		}
		if err != nil && err != io.EOF {
			return 0, err
		}
		select {
		case <-g.stop:
			return 0, io.EOF
		case <-time.After(time.Millisecond):
		}
	}
}

func (g *growing) Close() error { return g.f.Close() }

// streamFetcher hands back a growing reader rather than a finished file.
type streamFetcher struct {
	path string
	stop chan struct{}
}

func (s *streamFetcher) Local(context.Context, *queue.Item) (string, error) {
	return s.path, nil
}

func (s *streamFetcher) Stream(_ context.Context, _ *queue.Item) (io.ReadCloser, string, error) {
	f, err := os.Open(s.path)
	if err != nil {
		return nil, "", err
	}
	return &growing{f: f, stop: s.stop}, filepath.Ext(s.path), nil
}

// The one that would take the program down. beep's mp3 decoder documents that
// "the Seek method will panic if rc is not io.Seeker", and the seek bar is a
// drag away from calling it — from the audio path, where a panic is the end of
// the process rather than a message.
func TestSeekingAStreamCannotPanic(t *testing.T) {
	dir := t.TempDir()
	path := writeWAV(t, dir, "a.wav", 3)

	sink := &fakeSink{}
	p, err := New(sink)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	p.SetFetcher(&streamFetcher{path: path, stop: make(chan struct{})})

	p.SetQueue([]*queue.Item{{URL: "https://elsewhere/a.wav", Title: "streamed"}}, 0)
	waitPlaying(t, p)

	if p.Seekable() {
		t.Fatal("a stream reported itself as seekable")
	}
	// Would panic if the guard were missing, and the panic would not be caught
	// here — it would take the test binary with it, which is the point.
	p.Seek(1.5)

	if elapsed, _ := p.Position(); elapsed > 1.0 {
		t.Errorf("position moved to %.2f — the seek was not refused", elapsed)
	}
}

// A track on this device is opened as a file, so it keeps every bit of its
// scrubbing. Only what is still arriving gives that up.
func TestALocalTrackIsStillSeekable(t *testing.T) {
	dir := t.TempDir()
	path := writeWAV(t, dir, "a.wav", 3)

	sink := &fakeSink{}
	p, err := New(sink)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	p.SetQueue([]*queue.Item{{Path: path}}, 0)
	waitPlaying(t, p)

	if !p.Seekable() {
		t.Fatal("a local file was not seekable")
	}
	p.Seek(1.5)
	if elapsed, _ := p.Position(); elapsed < 1.4 || elapsed > 1.6 {
		t.Errorf("seeked to %.2f, want ~1.5", elapsed)
	}
}

// Nothing playing seeks nowhere, and must not reach into a nil source.
func TestSeekableWithNothingPlaying(t *testing.T) {
	p, err := New(&fakeSink{})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if p.Seekable() {
		t.Error("an idle player claimed to be seekable")
	}
	p.Seek(10) // must not panic
}
