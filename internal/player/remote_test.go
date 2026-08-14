package player

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"daemonlord.ygg/madplayer/internal/queue"
)

// fakeFetcher stands in for the download. It hands back a file that is already
// on disk, so these tests are about WHEN the player resolves a remote item, not
// about the network.
type fakeFetcher struct {
	path string
	err  error

	block   chan struct{} // closed to let a fetch finish
	calls   atomic.Int32
	stopped chan struct{} // closed when a fetch is cancelled
}

// Stream is what the player actually calls now. It reuses Local so every test
// here keeps testing WHEN the player resolves a remote item — a file already on
// disk seeks, which is exactly the "already cached" case.
func (f *fakeFetcher) Stream(ctx context.Context, item *queue.Item) (io.ReadCloser, string, error) {
	path, err := f.Local(ctx, item)
	if err != nil {
		return nil, "", err
	}
	rc, err := os.Open(path)
	return rc, filepath.Ext(path), err
}

func (f *fakeFetcher) Local(ctx context.Context, item *queue.Item) (string, error) {
	f.calls.Add(1)
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			if f.stopped != nil {
				close(f.stopped)
			}
			return "", ctx.Err()
		}
	}
	if f.err != nil {
		return "", f.err
	}
	return f.path, nil
}

func TestARemoteTrackIsFetchedThenPlayed(t *testing.T) {
	dir := t.TempDir()
	a := writeWAV(t, dir, "a.wav", 0.05)

	sink := &fakeSink{}
	p, _ := New(sink)
	defer p.Close()
	fetch := &fakeFetcher{path: a}
	p.SetFetcher(fetch)

	p.SetQueue([]*queue.Item{{URL: "http://host/files/abc/a.wav", Title: "Archangel"}}, 0)
	waitPlaying(t, p)

	if n := fetch.calls.Load(); n != 1 {
		t.Errorf("fetched %d times, want 1", n)
	}
	if _, total := p.Position(); total <= 0 {
		t.Error("the fetched file is not open for playback")
	}
}

// Resolving inline would freeze the window for the length of a download, which
// is the whole reason loading moved off the caller's goroutine.
func TestStartingARemoteTrackDoesNotBlockTheCaller(t *testing.T) {
	sink := &fakeSink{}
	p, _ := New(sink)
	defer p.Close()
	fetch := &fakeFetcher{path: "", block: make(chan struct{})}
	p.SetFetcher(fetch)

	done := make(chan struct{})
	go func() {
		p.SetQueue([]*queue.Item{{URL: "http://host/files/abc/a.wav"}}, 0)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("SetQueue blocked on a download; the UI thread would be frozen")
	}

	waitFor(t, "the download to be announced", p.Loading)
	close(fetch.block)
}

// Skipping through a queue must abandon what it passed, or ten skips are ten
// downloads.
func TestSwitchingTracksCancelsTheDownloadInFlight(t *testing.T) {
	dir := t.TempDir()
	local := writeWAV(t, dir, "local.wav", 0.5)

	sink := &fakeSink{}
	p, _ := New(sink)
	defer p.Close()
	fetch := &fakeFetcher{block: make(chan struct{}), stopped: make(chan struct{})}
	p.SetFetcher(fetch)

	p.SetQueue([]*queue.Item{
		{URL: "http://host/files/abc/remote.wav"},
		{Path: local},
	}, 0)
	waitFor(t, "the download to start", func() bool { return fetch.calls.Load() == 1 })

	p.Next() // the user moved on

	select {
	case <-fetch.stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("the abandoned download kept running")
	}
	waitPlaying(t, p)
	if got := p.Current().Path; got != local {
		t.Errorf("playing %q, want the local track", got)
	}
	// A cancelled download is the user skipping, not a broken track: flagging
	// the row would accuse a file that is perfectly fine.
	if err := p.Unplayable(queue.Key("", "http://host/files/abc/remote.wav")); err != nil {
		t.Errorf("the skipped track was marked unplayable: %v", err)
	}
}

// A download that fails is a track that cannot play: mark it and move on, the
// same contract a corrupt local file has.
func TestAFailedDownloadMarksTheRowAndAdvances(t *testing.T) {
	dir := t.TempDir()
	good := writeWAV(t, dir, "good.wav", 0.05)

	sink := &fakeSink{}
	p, _ := New(sink)
	defer p.Close()
	p.SetFetcher(&fakeFetcher{err: errors.New("connection refused")})

	url := "http://host/files/abc/gone.wav"
	p.SetQueue([]*queue.Item{{URL: url}, {Path: good}}, 0)

	waitFor(t, "the queue to skip the unreachable track", func() bool { return p.QueueIndex() == 1 })
	if p.Unplayable(queue.Key("", url)) == nil {
		t.Error("the unreachable track is not marked; its row cannot be flagged")
	}
	if p.TakeError() == nil {
		t.Error("no error to report to the user")
	}
}

// With nothing wired up to download, a remote track must say what is wrong
// rather than fail obscurely.
func TestARemoteTrackWithNoFetcherSaysSo(t *testing.T) {
	sink := &fakeSink{}
	p, _ := New(sink)
	defer p.Close()

	url := "http://host/files/abc/a.wav"
	p.SetQueue([]*queue.Item{{URL: url}}, 0)

	waitFor(t, "the failure to be recorded", func() bool {
		return p.Unplayable(queue.Key("", url)) != nil
	})
	if err := p.Unplayable(queue.Key("", url)); err == nil || !contains(err.Error(), "server") {
		t.Errorf("err = %v, want it to name the problem", err)
	}
}

// A local file is not a download, and must not flash a "loading" state.
func TestALocalTrackIsNotAnnouncedAsLoading(t *testing.T) {
	dir := t.TempDir()
	a := writeWAV(t, dir, "a.wav", 0.5)

	sink := &fakeSink{}
	p, _ := New(sink)
	defer p.Close()

	p.SetQueue([]*queue.Item{{Path: a}}, 0)
	if p.Loading() {
		t.Error("a local file reported as loading")
	}
	waitPlaying(t, p)
	if p.Loading() {
		t.Error("still loading after playback started")
	}
}

func TestQueueKeyDistinguishesLocalFromRemote(t *testing.T) {
	local := &queue.Item{Path: "/music/a.flac"}
	remote := &queue.Item{URL: "http://host/files/abc/a.mp3"}

	if local.RowKey() == remote.RowKey() {
		t.Error("a local and a remote row share a key; the wrong row would highlight")
	}
	if local.Remote() {
		t.Error("a track with local bytes is not remote")
	}
	if !remote.Remote() {
		t.Error("a track with only a URL is remote")
	}
	// Both ways of naming the same file agree, so a row and its queue item match.
	if (&queue.Item{Path: "/music/a.flac"}).RowKey() != queue.Key("/music/a.flac", "") {
		t.Error("Key and RowKey disagree")
	}
	if (&queue.Item{}).Playable() {
		t.Error("an item naming no audio is not playable")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
