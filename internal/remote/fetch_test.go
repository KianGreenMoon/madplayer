package remote

import (
	"context"
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

const hashA = "3f786850e387550fdab836ed7e6dc881de23001b3f786850e387550fdab836ed"

// audioServer serves one blob and counts how often it was asked for.
func audioServer(t *testing.T, body string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/files/") {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		hits.Add(1)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func fetcher(t *testing.T, srv *httptest.Server) *Fetcher {
	t.Helper()
	cache, err := blobcache.Open(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	f := New(cache, quiet())
	f.SetServers([]library.Server{{
		Base:   srv.URL,
		Label:  "host",
		Client: madshare.New(srv.URL, "tok"),
	}})
	return f
}

func TestLocalDownloadsThenServesFromDisk(t *testing.T) {
	srv, hits := audioServer(t, "FLAC bytes")
	f := fetcher(t, srv)
	item := &queue.Item{URL: srv.URL + "/files/" + hashA + "/song.flac", Hash: hashA, Origin: "host"}

	path, err := f.Local(context.Background(), item)
	if err != nil {
		t.Fatalf("Local: %v", err)
	}
	if got, _ := os.ReadFile(path); string(got) != "FLAC bytes" {
		t.Errorf("contents = %q", got)
	}
	// The extension decides the decoder, so it has to survive the round trip.
	if !strings.HasSuffix(path, ".flac") {
		t.Errorf("cached as %q, want the container's extension kept", path)
	}

	if _, err := f.Local(context.Background(), item); err != nil {
		t.Fatalf("second Local: %v", err)
	}
	if n := hits.Load(); n != 1 {
		t.Errorf("server hit %d times, want 1 — the second play should come off disk", n)
	}
	if !f.Cached(item) {
		t.Error("Cached should report a track that is on disk")
	}
}

// Two servers offering the same audio are one file on disk: the cache is keyed
// by content, not by address.
func TestTheSameAudioFromTwoServersIsCachedOnce(t *testing.T) {
	one, hitsOne := audioServer(t, "same bytes")
	two, hitsTwo := audioServer(t, "same bytes")

	cache, err := blobcache.Open(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	f := New(cache, quiet())
	f.SetServers([]library.Server{
		{Base: one.URL, Label: "one", Client: madshare.New(one.URL, "tok")},
		{Base: two.URL, Label: "two", Client: madshare.New(two.URL, "tok")},
	})

	a := &queue.Item{URL: one.URL + "/files/" + hashA + "/song.mp3", Hash: hashA}
	b := &queue.Item{URL: two.URL + "/files/" + hashA + "/song.mp3", Hash: hashA}

	if _, err := f.Local(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Local(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	if hitsOne.Load()+hitsTwo.Load() != 1 {
		t.Errorf("downloaded %d times, want once for one piece of audio",
			hitsOne.Load()+hitsTwo.Load())
	}
}

func TestATrackWhoseServerIsGoneSaysSo(t *testing.T) {
	cache, err := blobcache.Open(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	f := New(cache, quiet())

	_, err = f.Local(context.Background(), &queue.Item{URL: "http://gone:3000/files/x/a.mp3"})
	if err == nil || !strings.Contains(err.Error(), "signed in") {
		t.Errorf("err = %v, want it to name the missing sign-in", err)
	}
}

// A revoked token and a deleted track are different problems with different
// answers, and "401" tells the person neither.
func TestServerRefusalsAreTranslated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "revoked"):
			http.Error(w, "authentication required", http.StatusUnauthorized)
		case strings.Contains(r.URL.Path, "forbidden"):
			http.Error(w, "forbidden", http.StatusForbidden)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	f := fetcher(t, srv)

	cases := map[string]string{
		"revoked":   "sign in again",
		"forbidden": "may not play",
		"missing":   "no longer has this track",
	}
	for path, want := range cases {
		item := &queue.Item{URL: srv.URL + "/files/" + path + "/a.mp3", Origin: "host"}
		_, err := f.Local(context.Background(), item)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want it to mention %q", path, err, want)
		}
	}
}

// The longest matching base wins: madshare behind a path on a reverse proxy is a
// real deployment, and the bare host must not shadow it.
func TestTheLongestMatchingServerWins(t *testing.T) {
	cache, _ := blobcache.Open(t.TempDir(), 0)
	f := New(cache, quiet())
	bare := madshare.New("http://host:3000", "bare")
	under := madshare.New("http://host:3000/madshare", "under")
	f.SetServers([]library.Server{
		{Base: "http://host:3000", Label: "bare", Client: bare},
		{Base: "http://host:3000/madshare", Label: "under", Client: under},
	})

	got, err := f.serverFor("http://host:3000/madshare/files/abc/a.mp3")
	if err != nil {
		t.Fatal(err)
	}
	if got.Client != under {
		t.Error("the bare host shadowed the server mounted under a path")
	}
}

func TestPrefetchWarmsTheCacheAndIsIdempotent(t *testing.T) {
	srv, hits := audioServer(t, "bytes")
	f := fetcher(t, srv)
	item := &queue.Item{URL: srv.URL + "/files/" + hashA + "/song.mp3", Hash: hashA}

	f.Prefetch(item)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !f.Cached(item) {
		time.Sleep(5 * time.Millisecond)
	}
	if !f.Cached(item) {
		t.Fatal("the prefetched track never landed in the cache")
	}

	// Safe to call on every change the player reports: an already-cached track
	// costs nothing.
	f.Prefetch(item)
	f.Prefetch(item)
	if n := hits.Load(); n != 1 {
		t.Errorf("downloaded %d times, want 1", n)
	}

	// A local track is not a download.
	f.Prefetch(&queue.Item{Path: "/music/a.flac"})
	f.StopPrefetch()
}
