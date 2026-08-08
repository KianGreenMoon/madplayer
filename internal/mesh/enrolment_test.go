package mesh

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"daemonlord.ygg/madplayer/internal/madshare"
)

// Enrolment is what stands in for being in a graph. These are about the round:
// what it asks for, in what order, and what it does when one part of it is
// refused — because a phone's network comes and goes and none of the answers are
// durable.

// fakeNode records what enrolment did to this device's mesh surface.
type fakeNode struct {
	mu       sync.Mutex
	token    string
	peers    []string
	homes    map[string]string // key → base URL
	removed  []string
	holdings []string
}

func newFakeNode(holdings ...string) *fakeNode {
	return &fakeNode{homes: map[string]string{}, holdings: holdings}
}

func (f *fakeNode) Key() string { return strings.Repeat("ab", 32) }
func (f *fakeNode) SetToken(t string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.token = t
}
func (f *fakeNode) AddPeer(uri string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.peers = append(f.peers, uri)
	return nil
}
func (f *fakeNode) AddHome(_ context.Context, key, base, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.homes[key] = base
	return nil
}
func (f *fakeNode) RemoveHome(_ context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.homes, key)
	f.removed = append(f.removed, key)
	return nil
}
func (f *fakeNode) Holdings() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.holdings...)
}

func (f *fakeNode) snapshot() fakeNode {
	f.mu.Lock()
	defer f.mu.Unlock()
	return fakeNode{token: f.token, peers: append([]string(nil), f.peers...),
		homes: map[string]string{}, removed: append([]string(nil), f.removed...)}
}

// homeServer is a madshare that answers the three mesh calls.
type homeServer struct {
	*httptest.Server
	issuer string

	mu       sync.Mutex
	pushed   []string
	pushes   int
	noPeers  bool // 404 the peering endpoint, as an operator who switched it off
	tokenErr bool
}

func newHomeServer(t *testing.T, issuer string) *homeServer {
	t.Helper()
	h := &homeServer{issuer: issuer}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/madnetwork/token", func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		fail := h.tokenErr
		h.mu.Unlock()
		if fail {
			http.Error(w, "no", http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(madshare.Grant{
			Token:      "token-for-" + h.issuer,
			Issuer:     h.issuer,
			ExpiresAt:  time.Now().Add(time.Hour),
			RenewAfter: time.Now().Add(30 * time.Minute),
		})
	})
	mux.HandleFunc("/api/madnetwork/peering", func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		off := h.noPeers
		h.mu.Unlock()
		if off {
			http.Error(w, "this node does not share its peering", http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(madshare.Peering{
			Listen: []string{"tls://" + h.issuer + ".example:1"},
		})
	})
	mux.HandleFunc("/api/madnetwork/holdings", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Hashes []string `json:"hashes"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		h.mu.Lock()
		h.pushed, h.pushes = body.Hashes, h.pushes+1
		h.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "refresh_after": 2700})
	})
	h.Server = httptest.NewServer(mux)
	t.Cleanup(h.Close)
	return h
}

func (h *homeServer) server(label string) Server {
	return Server{Base: h.URL, Label: label, Client: madshare.New(h.URL, "api-token")}
}

func quiet() *log.Logger { return log.New(io.Discard, "", 0) }

// TestEnrolmentDoesTheWholeRound: a vouch, a way onto the underlay, and an
// advertisement — and the issuer recorded as a home node, which is what lets the
// server and its other devices fetch from here.
func TestEnrolmentDoesTheWholeRound(t *testing.T) {
	node := newFakeNode("hash-one", "hash-two")
	home := newHomeServer(t, "aaaa")
	e := New(node, quiet())
	ctx := context.Background()
	e.SetServers(ctx, []Server{home.server("home")})

	e.round(ctx)

	got := node.snapshot()
	if got.token != "token-for-aaaa" {
		t.Errorf("installed token = %q, want the one this server issued", got.token)
	}
	node.mu.Lock()
	base, recorded := node.homes["aaaa"]
	node.mu.Unlock()
	if !recorded || base != home.URL {
		t.Errorf("home nodes = %v, want the issuer recorded against %s", node.homes, home.URL)
	}
	if len(got.peers) != 1 || got.peers[0] != "tls://aaaa.example:1" {
		t.Errorf("peers dialled = %v, want the one the server published", got.peers)
	}
	home.mu.Lock()
	pushed := home.pushed
	home.mu.Unlock()
	if len(pushed) != 2 {
		t.Errorf("advertised %v, want both cached blobs", pushed)
	}

	st := e.Status()
	if len(st) != 1 || st[0].Key != "aaaa" || st[0].Advertised != 2 || st[0].Problem != "" {
		t.Errorf("status = %+v, want an enrolled server with no problem", st)
	}
}

// TestEnrolmentSurvivesAServerThatSharesNoPeering: a 404 there is an answer, not
// a failure. This device has two other ways onto the mesh, and the round must
// still leave it vouched for and advertised.
func TestEnrolmentSurvivesAServerThatSharesNoPeering(t *testing.T) {
	node := newFakeNode("hash-one")
	home := newHomeServer(t, "bbbb")
	home.noPeers = true
	e := New(node, quiet())
	ctx := context.Background()
	e.SetServers(ctx, []Server{home.server("home")})

	e.round(ctx)

	if st := e.Status(); len(st) != 1 || st[0].Problem != "" {
		t.Errorf("status = %+v, want no problem — sharing off is a decision, not a fault", st)
	}
	if got := node.snapshot(); got.token == "" || len(got.peers) != 0 {
		t.Errorf("token=%q peers=%v, want a vouch and no peers", got.token, got.peers)
	}
}

// TestEnrolmentReportsAServerItCannotReach, and comes back to it: a phone's
// network comes and goes, and the retry is much shorter than the deadlines it is
// defending.
func TestEnrolmentReportsAServerItCannotReach(t *testing.T) {
	node := newFakeNode()
	home := newHomeServer(t, "cccc")
	home.tokenErr = true
	e := New(node, quiet())
	ctx := context.Background()
	e.SetServers(ctx, []Server{home.server("home")})

	e.round(ctx)
	st := e.Status()
	if len(st) != 1 || st[0].Problem == "" {
		t.Fatalf("status = %+v, want a problem to show", st)
	}
	if st[0].Key != "" {
		t.Error("a server that refused a token was recorded as a home node anyway")
	}
	// Not retried immediately — that would hammer a server that is down.
	home.mu.Lock()
	home.tokenErr = false
	home.mu.Unlock()
	e.round(ctx)
	if st := e.Status(); st[0].Key != "" {
		t.Error("a failed server was retried within the backoff")
	}
}

// TestEnrolmentForgetsAServerOnSigningOut: signing out of a server means this
// device stops taking its word about strangers, which is a mesh fact and not
// only a UI one.
func TestEnrolmentForgetsAServerOnSigningOut(t *testing.T) {
	node := newFakeNode()
	home := newHomeServer(t, "dddd")
	e := New(node, quiet())
	ctx := context.Background()
	e.SetServers(ctx, []Server{home.server("home")})
	e.round(ctx)

	e.SetServers(ctx, nil)
	got := node.snapshot()
	if len(got.removed) != 1 || got.removed[0] != "dddd" {
		t.Errorf("removed home nodes = %v, want the one signed out of", got.removed)
	}
	if len(e.Status()) != 0 {
		t.Errorf("status still lists %d server(s)", len(e.Status()))
	}
}

// TestPresentPicksOneServersToken is the constraint the whole design has to live
// with: the mesh carries ONE vouch, and a third node checks it against an issuer
// it can place. So the token installed must be the one from the server that
// named the holders being fetched from.
func TestPresentPicksOneServersToken(t *testing.T) {
	node := newFakeNode()
	one, two := newHomeServer(t, "1111"), newHomeServer(t, "2222")
	e := New(node, quiet())
	ctx := context.Background()
	e.SetServers(ctx, []Server{one.server("one"), two.server("two")})
	e.round(ctx)

	if !e.Present(two.URL) {
		t.Fatal("Present reported no token for a server that issued one")
	}
	if got := node.snapshot(); got.token != "token-for-2222" {
		t.Errorf("installed token = %q, want the second server's", got.token)
	}
	if !e.Present(one.URL) {
		t.Fatal("Present reported no token for the first server")
	}
	if got := node.snapshot(); got.token != "token-for-1111" {
		t.Errorf("installed token = %q, want the first server's", got.token)
	}
	if e.Present("http://never.signed.in") {
		t.Error("Present claimed a token for a server this device does not know")
	}
}
