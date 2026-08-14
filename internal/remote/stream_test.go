package remote

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"daemonlord.ygg/madplayer/internal/blobcache"
	"daemonlord.ygg/madplayer/internal/library"
	"daemonlord.ygg/madplayer/internal/madshare"
	"daemonlord.ygg/madplayer/internal/queue"
)

// Streaming, end to end through the real relay path.
//
// The unit tests below the cache prove the reader; this proves the join — that
// asking the fetcher for a track hands back bytes while the server is still
// sending them, rather than after.

// slowServer dribbles a body out at a fixed rate, which is what makes the
// difference between "streamed" and "downloaded" observable at all. A fast link
// hides it: measured against the live server, 29.5 MB arrived in 4.7 s, and the
// same track over the swarm took minutes — which is the case this is for.
func slowServer(t *testing.T, size, chunk int, gap time.Duration) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var sent atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/files/") {
			http.NotFound(w, r)
			return
		}
		flusher, _ := w.(http.Flusher)
		buf := make([]byte, chunk)
		for done := 0; done < size; done += chunk {
			n := chunk
			if done+n > size {
				n = size - done
			}
			if _, err := w.Write(buf[:n]); err != nil {
				return
			}
			sent.Add(int64(n))
			if flusher != nil {
				flusher.Flush()
			}
			select {
			case <-r.Context().Done():
				return
			case <-time.After(gap):
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &sent
}

func slowFetcher(t *testing.T, srv *httptest.Server) *Fetcher {
	t.Helper()
	cache, err := blobcache.Open(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	f := New(cache, quiet())
	f.SetServers([]library.Server{{Base: srv.URL, Label: "host", Client: madshare.New(srv.URL, "tok")}})
	return f
}

// The claim, at the level the player sees it: bytes arrive while the server is
// still sending. Before this, Local waited for the last byte and a remote track
// was silent for the whole download.
func TestStreamHandsOverBytesWhileTheServerIsStillSending(t *testing.T) {
	// 40 pieces, 25ms apart: about a second in total.
	srv, sent := slowServer(t, 40<<10, 1<<10, 25*time.Millisecond)
	f := slowFetcher(t, srv)
	item := &queue.Item{URL: srv.URL + "/files/abc/song.mp3", Hash: "abc"}

	start := time.Now()
	rc, ext, err := f.Stream(context.Background(), item)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	if ext != ".mp3" {
		t.Errorf("ext = %q, want .mp3 — the decoder is chosen by it", ext)
	}

	if _, err := io.ReadFull(rc, make([]byte, 1<<10)); err != nil {
		t.Fatalf("first read: %v", err)
	}
	elapsed := time.Since(start)
	atFirstRead := sent.Load()

	if elapsed > 500*time.Millisecond {
		t.Errorf("first bytes after %v — that is most of the transfer, not a stream", elapsed)
	}
	if atFirstRead >= 40<<10 {
		t.Errorf("the server had already sent everything (%d bytes) before the first read", atFirstRead)
	}
	t.Logf("first bytes after %v, with %d of %d bytes sent", elapsed.Round(time.Millisecond), atFirstRead, 40<<10)
}

// A stream must not seek, or the decoders take their whole-file path and the
// wait comes straight back.
func TestAStreamedTrackDoesNotSeek(t *testing.T) {
	srv, _ := slowServer(t, 4<<10, 1<<10, time.Millisecond)
	f := slowFetcher(t, srv)

	rc, _, err := f.Stream(context.Background(), &queue.Item{URL: srv.URL + "/files/abc/song.mp3", Hash: "abc"})
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	if _, ok := rc.(io.Seeker); ok {
		t.Error("a streamed track seeks — the decoders would wait for the whole file")
	}
}

// Once it is on disk it is an ordinary file again, so replaying a track keeps
// its scrubbing.
func TestAReplayedTrackSeeksAgain(t *testing.T) {
	srv, _ := slowServer(t, 4<<10, 4<<10, 0)
	f := slowFetcher(t, srv)
	item := &queue.Item{URL: srv.URL + "/files/abc/song.mp3", Hash: "abc"}

	if _, err := f.Local(context.Background(), item); err != nil {
		t.Fatal(err)
	}
	rc, _, err := f.Stream(context.Background(), item)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	if _, ok := rc.(io.Seeker); !ok {
		t.Error("a cached track came back unseekable")
	}
}

// A track on this device never goes near the network, streamed or not.
func TestALocalPathIsOpenedDirectly(t *testing.T) {
	srv, sent := slowServer(t, 1<<10, 1<<10, 0)
	f := slowFetcher(t, srv)

	dir := t.TempDir()
	path := dir + "/mine.flac"
	if err := os.WriteFile(path, []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	rc, ext, err := f.Stream(context.Background(), &queue.Item{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	if ext != ".flac" {
		t.Errorf("ext = %q", ext)
	}
	if sent.Load() != 0 {
		t.Error("a local track reached the server")
	}
}
