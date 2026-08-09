package remote

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"daemonlord.ygg/madplayer/internal/blobcache"
	"daemonlord.ygg/madplayer/internal/library"
	"daemonlord.ygg/madplayer/internal/madshare"
	"daemonlord.ygg/madplayer/internal/queue"
)

// quiet is the logger for tests that are meant to log: a swarm falling back to
// the relay says so on purpose, and a passing test should not print it.
func quiet() *log.Logger { return log.New(io.Discard, "", 0) }

// meshServer is a home server that both names holders and relays bytes, so a
// test can see which of the two a fetch actually used.
type meshServer struct {
	*httptest.Server
	relay   atomic.Int32 // GET /files/…             — the level-1 download
	holders atomic.Int32 // GET /api/madnetwork/holders/…
	keys    []string     // who it says holds the blob
	size    int64
	// lastSeen ages a holder, so a test can hand the client the kind of plan a
	// real server sends: freshest first, with nodes that have been gone for days
	// still on it.
	lastSeen map[string]int64
}

func newMeshServer(t *testing.T, body string, keys ...string) *meshServer {
	t.Helper()
	ms := &meshServer{keys: keys, size: int64(len(body))}
	ms.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/madnetwork/holders/"):
			ms.holders.Add(1)
			hs := make([]map[string]any, 0, len(ms.keys))
			for _, k := range ms.keys {
				hs = append(hs, map[string]any{
					"key": k, "name": "a device", "last_seen": ms.lastSeen[k],
				})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"hash":    strings.TrimPrefix(r.URL.Path, "/api/madnetwork/holders/"),
				"size":    ms.size,
				"holders": hs,
			})
		case strings.HasPrefix(r.URL.Path, "/files/"):
			ms.relay.Add(1)
			_, _ = w.Write([]byte(body))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(ms.Close)
	return ms
}

// hashB is a second blob, for the one test that needs two fetches the cache's
// own single-flight will not collapse into one.
const hashB = "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"

func (ms *meshServer) track(hash string) *queue.Item {
	return &queue.Item{URL: ms.URL + "/files/" + hash + "/song.flac", Hash: hash, Origin: "home"}
}

func meshFetcher(t *testing.T, ms *meshServer, sw Swarm, v Vouch) *Fetcher {
	t.Helper()
	cache, err := blobcache.Open(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	f := New(cache, quiet())
	f.SetServers([]library.Server{{
		Base:   ms.URL,
		Label:  "home",
		Client: madshare.New(ms.URL, "tok"),
	}})
	f.SetSwarm(sw, v)
	return f
}

// fakeSwarm stands in for the embedded node's mesh fetch.
type fakeSwarm struct {
	body   string
	stream io.Reader // overrides body, for a fetch that dies part-way
	err    error

	mu      sync.Mutex
	calls   int
	hash    string
	size    int64
	holders []string

	// gate, when non-nil, holds a fetch open so a test can watch what a second
	// one does while the first is still running.
	gate <-chan struct{}
	live atomic.Int32
	peak atomic.Int32
}

func (s *fakeSwarm) FetchBlob(ctx context.Context, hash string, size int64, holders []string) (io.ReadCloser, error) {
	n := s.live.Add(1)
	defer s.live.Add(-1)
	for {
		peak := s.peak.Load()
		if n <= peak || s.peak.CompareAndSwap(peak, n) {
			break
		}
	}

	s.mu.Lock()
	s.calls++
	s.hash, s.size, s.holders = hash, size, holders
	s.mu.Unlock()

	if s.gate != nil {
		select {
		case <-s.gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if s.err != nil {
		return nil, s.err
	}
	if s.stream != nil {
		return io.NopCloser(s.stream), nil
	}
	return io.NopCloser(strings.NewReader(s.body)), nil
}

// fakeVouch records which server's token was asked for.
type fakeVouch struct {
	ok bool

	mu    sync.Mutex
	bases []string
}

func (v *fakeVouch) Present(base string) bool {
	v.mu.Lock()
	v.bases = append(v.bases, base)
	v.mu.Unlock()
	return v.ok
}

func (v *fakeVouch) seen() []string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return append([]string(nil), v.bases...)
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSwarmIsPreferredToTheRelay(t *testing.T) {
	ms := newMeshServer(t, "RELAY bytes", "aa11", "bb22")
	sw := &fakeSwarm{body: "SWARM bytes"}
	f := meshFetcher(t, ms, sw, &fakeVouch{ok: true})

	path, err := f.Local(context.Background(), ms.track(hashA))
	if err != nil {
		t.Fatal(err)
	}
	if got := read(t, path); got != "SWARM bytes" {
		t.Fatalf("played %q, want the swarm's copy", got)
	}
	if n := ms.relay.Load(); n != 0 {
		t.Fatalf("relay hit %d time(s); the swarm had it", n)
	}
	if sw.hash != hashA || sw.size != ms.size {
		t.Fatalf("fetched %q/%d, want %q/%d", sw.hash, sw.size, hashA, ms.size)
	}
	// The holders the server named, not a guess: the endpoint is the only source
	// of them on this client.
	if want := []string{"aa11", "bb22"}; !equal(sw.holders, want) {
		t.Fatalf("asked holders %v, want %v", sw.holders, want)
	}
}

func TestRelayTakesOverWhenTheSwarmFails(t *testing.T) {
	ms := newMeshServer(t, "RELAY bytes", "aa11")
	sw := &fakeSwarm{err: errors.New("nobody answered")}
	f := meshFetcher(t, ms, sw, &fakeVouch{ok: true})

	path, err := f.Local(context.Background(), ms.track(hashA))
	if err != nil {
		t.Fatal(err)
	}
	if got := read(t, path); got != "RELAY bytes" {
		t.Fatalf("played %q, want the relay's copy", got)
	}
	if n := ms.relay.Load(); n != 1 {
		t.Fatalf("relay hit %d time(s), want 1", n)
	}
}

func TestRelayTakesOverWhenNobodyHoldsIt(t *testing.T) {
	ms := newMeshServer(t, "RELAY bytes") // an empty holder list is a normal answer
	sw := &fakeSwarm{body: "SWARM bytes"}
	f := meshFetcher(t, ms, sw, &fakeVouch{ok: true})

	path, err := f.Local(context.Background(), ms.track(hashA))
	if err != nil {
		t.Fatal(err)
	}
	if got := read(t, path); got != "RELAY bytes" {
		t.Fatalf("played %q, want the relay's copy", got)
	}
	if sw.calls != 0 {
		t.Fatalf("swarm fetched %d time(s) with no holders to fetch from", sw.calls)
	}
}

func TestSwarmPresentsTheVouchOfTheServerThatNamedTheHolders(t *testing.T) {
	ms := newMeshServer(t, "RELAY bytes", "aa11")
	v := &fakeVouch{ok: true}
	f := meshFetcher(t, ms, &fakeSwarm{body: "SWARM bytes"}, v)

	if _, err := f.Local(context.Background(), ms.track(hashA)); err != nil {
		t.Fatal(err)
	}
	if seen := v.seen(); len(seen) != 1 || seen[0] != ms.URL {
		t.Fatalf("presented %v, want the token from %s", seen, ms.URL)
	}
}

func TestNoVouchMeansTheRelay(t *testing.T) {
	// Enrolment has not had a successful round with this server yet. The device
	// is still signed in over HTTP, so the download works — it just is not a mesh
	// download.
	ms := newMeshServer(t, "RELAY bytes", "aa11")
	sw := &fakeSwarm{body: "SWARM bytes"}
	f := meshFetcher(t, ms, sw, &fakeVouch{ok: false})

	path, err := f.Local(context.Background(), ms.track(hashA))
	if err != nil {
		t.Fatal(err)
	}
	if got := read(t, path); got != "RELAY bytes" {
		t.Fatalf("played %q, want the relay's copy", got)
	}
	if sw.calls != 0 {
		t.Fatalf("swarm fetched %d time(s) with no vouch to present", sw.calls)
	}
	if n := ms.holders.Load(); n != 0 {
		t.Fatalf("asked for holders %d time(s) before having a vouch", n)
	}
}

func TestATrackWithNoHashSkipsTheSwarm(t *testing.T) {
	// A blob is addressed by its hash on the mesh, so a track the server named
	// without one cannot be fetched that way at all.
	ms := newMeshServer(t, "RELAY bytes", "aa11")
	sw := &fakeSwarm{body: "SWARM bytes"}
	f := meshFetcher(t, ms, sw, &fakeVouch{ok: true})

	item := ms.track(hashA)
	item.Hash = ""
	path, err := f.Local(context.Background(), item)
	if err != nil {
		t.Fatal(err)
	}
	if got := read(t, path); got != "RELAY bytes" {
		t.Fatalf("played %q, want the relay's copy", got)
	}
	if sw.calls != 0 {
		t.Fatalf("swarm fetched %d time(s) for a track with no hash", sw.calls)
	}
}

func TestMeshFetchesRunOneAtATime(t *testing.T) {
	// The mesh carries ONE vouch, installed process-wide, so a second fetch
	// starting underneath the first would swap the token out from under it.
	ms := newMeshServer(t, "RELAY bytes", "aa11")
	gate := make(chan struct{})
	sw := &fakeSwarm{body: "SWARM bytes", gate: gate}
	f := meshFetcher(t, ms, sw, &fakeVouch{ok: true})

	// Two different BLOBS, so what is being measured is this package's own
	// serialization rather than the cache's single-flight — which would fold two
	// requests for one hash into a single fetch whatever the mesh did.
	one, two := ms.track(hashA), ms.track(hashB)

	var wg sync.WaitGroup
	for _, item := range []*queue.Item{one, two} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := f.Local(context.Background(), item); err != nil {
				t.Error(err)
			}
		}()
	}
	// Let both reach the swarm before either is allowed to finish. If they could
	// overlap, this is when they would.
	time.Sleep(50 * time.Millisecond)
	close(gate)
	wg.Wait()

	if sw.calls != 2 {
		t.Fatalf("swarm fetched %d time(s), want 2", sw.calls)
	}
	if peak := sw.peak.Load(); peak != 1 {
		t.Fatalf("%d mesh fetches ran at once, want 1", peak)
	}
}

func TestAPartlyWrittenSwarmFetchIsNotRetriedOverTheRelay(t *testing.T) {
	// Falling back after bytes have already landed would append a second source's
	// copy to the first's, and the result would decode as noise rather than fail.
	ms := newMeshServer(t, "RELAY bytes", "aa11")
	sw := &fakeSwarm{stream: io.MultiReader(
		strings.NewReader("SWARM half"),
		errReader{errors.New("the holder went away")},
	)}
	f := meshFetcher(t, ms, sw, &fakeVouch{ok: true})

	if _, err := f.Local(context.Background(), ms.track(hashA)); err == nil {
		t.Fatal("a half-written fetch reported success")
	}
	if n := ms.relay.Load(); n != 0 {
		t.Fatalf("relay hit %d time(s) after the swarm had already written", n)
	}
}

// TestStaleHoldersReachTheSwarmUntouched is the client half of a cost measured
// against a live server on 2026-08-09.
//
// The plan that arrived named holders last seen 21 and 54 hours earlier, and
// each dead one costs the swarm four stall timeouts before it is retired — about
// ninety seconds of the four minutes those fetches took. With one live holder and
// no stale ones the same server delivered in 1m43s.
//
// The client passes the plan through exactly as given: which nodes are worth
// dialling is the server's call, it is the one with the graph, and a client that
// re-derives it disagrees with every other client. So this pins the pass-through
// rather than a filter, and it is the test that changes if a client-side cutoff
// is ever the answer.
func TestStaleHoldersReachTheSwarmUntouched(t *testing.T) {
	stale := time.Now().Add(-54 * time.Hour).Unix()
	ms := newMeshServer(t, "RELAY bytes", "live", "gone")
	ms.lastSeen = map[string]int64{"live": time.Now().Unix(), "gone": stale}

	sw := &fakeSwarm{body: "SWARM bytes"}
	f := meshFetcher(t, ms, sw, &fakeVouch{ok: true})

	path, err := f.Local(context.Background(), ms.track(hashA))
	if err != nil {
		t.Fatal(err)
	}
	if got := read(t, path); got != "SWARM bytes" {
		t.Fatalf("played %q, want the swarm's copy", got)
	}
	if want := []string{"live", "gone"}; !equal(sw.holders, want) {
		t.Fatalf("swarm asked %v, want %v — the server's plan, order and all", sw.holders, want)
	}
}

// A slow swarm must not cost the person their music. Measured against a real
// server, the relay delivered a 20 MB track in under four seconds and the swarm
// took four minutes for the same bytes — so a mesh attempt allowed to run to the
// caller's own deadline spends the whole allowance and leaves the relay a dead
// context, which is worse than never having had a mesh at all.
func TestASlowSwarmGivesWayToTheRelayWithTimeToSpare(t *testing.T) {
	ms := newMeshServer(t, "RELAY bytes", "aa11")
	stuck := make(chan struct{})
	defer close(stuck)
	sw := &fakeSwarm{body: "SWARM bytes", gate: stuck} // never delivers
	f := meshFetcher(t, ms, sw, &fakeVouch{ok: true})
	f.SetSwarmBudget(100 * time.Millisecond)

	// A caller's deadline generous enough for the relay and far short of forever:
	// if the swarm were allowed to eat it, this would fail rather than fall back.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	start := time.Now()
	path, err := f.Local(ctx, ms.track(hashA))
	if err != nil {
		t.Fatalf("a stalled swarm cost the track entirely: %v", err)
	}
	if got := read(t, path); got != "RELAY bytes" {
		t.Fatalf("played %q, want the relay's copy", got)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("took %s — the swarm was not bounded", took)
	}
	if sw.calls != 1 {
		t.Fatalf("swarm fetched %d time(s), want 1 attempt", sw.calls)
	}
}

type errReader struct{ err error }

func (r errReader) Read([]byte) (int, error) { return 0, r.err }

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
