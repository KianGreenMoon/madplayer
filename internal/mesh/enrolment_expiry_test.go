package mesh

// What enrolment does about a vouch whose life is over.
//
// These pin the defect behind "the madnetwork stopped playing after the laptop
// woke up" (found 2026-08-23, reproduced end to end in
// internal/backend/labnet_test.go): the enrolment records a grant's token and
// forgets its ExpiresAt. Present answers "is there a vouch" from the token's
// mere existence, so a token that died hours ago is still installed on the
// wire, every holder refuses it — deliberately without saying why — and a
// madnetwork track fails looking like nobody holds it. The honest decline the
// fetcher already carries ("this device has no vouch from X yet",
// remote/fetch.go) is unreachable, because Present never says no once it has
// ever said yes.
//
// Two ways a device gets there, both ordinary:
//
//   - the machine sleeps. The loop's tick, the due times and every time.Time
//     comparison here ride the MONOTONIC clock, which stands still during a
//     suspend on Linux (and an app freeze on Android) — so after eight hours
//     asleep the loop still believes renewal is half an hour away, while the
//     wall clock every verifier reads moved eight hours.
//   - the home server is unreachable past the token's life. fail() keeps the
//     old token on purpose ("valid until it expires" — true, and unchecked),
//     so once the hour passes the kept token is a corpse that Present still
//     presents.
//
// When the fix lands — Present (or the round) checking the grant's wall-clock
// expiry, refusing, and nudging a renewal — these tests are the ones to flip:
// each names the assertion that must then invert.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"daemonlord.ygg/madplayer/internal/madshare"
)

// expiryHome issues grants whose dates the test chooses, and can be told to
// start failing — a server that went unreachable.
type expiryHome struct {
	*httptest.Server
	mu    sync.Mutex
	grant madshare.Grant
	down  bool
}

func newExpiryHome(t *testing.T) *expiryHome {
	t.Helper()
	h := &expiryHome{}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/madnetwork/token", func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		down, g := h.down, h.grant
		h.mu.Unlock()
		if down {
			http.Error(w, "gone", http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(g)
	})
	mux.HandleFunc("/api/madnetwork/peering", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"peers": []string{}, "listen": []string{}})
	})
	mux.HandleFunc("/api/madnetwork/holdings", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"refresh_after": 3600})
	})
	h.Server = httptest.NewServer(mux)
	t.Cleanup(h.Close)
	return h
}

func (h *expiryHome) issue(g madshare.Grant) {
	h.mu.Lock()
	h.grant = g
	h.mu.Unlock()
}

func (h *expiryHome) setDown(v bool) {
	h.mu.Lock()
	h.down = v
	h.mu.Unlock()
}

// TestPresentStillOffersAVouchThatHasExpired: the post-sleep state. The grant
// enrolment holds is wall-clock dead while its RenewAfter — the only date the
// loop paces itself by — says "not due yet". Present answers yes and installs
// the corpse; nothing anywhere reports a problem. Every mesh fetch then runs
// with a vouch no verifier will honour.
//
// FLIP THIS when the expiry check lands: Present must answer false (the
// fetcher then declines with its honest "no vouch from X yet" instead of
// burning the swarm budget on refusals), and the round should be re-run
// rather than waited out.
func TestPresentStillOffersAVouchThatHasExpired(t *testing.T) {
	home := newExpiryHome(t)
	// Dead for half an hour, renewal supposedly half an hour away — exactly
	// what the monotonic clocks leave behind after a long suspend.
	home.issue(madshare.Grant{
		Token:      "expired-vouch",
		Issuer:     "issuer-key",
		ExpiresAt:  time.Now().Add(-30 * time.Minute),
		RenewAfter: time.Now().Add(30 * time.Minute),
	})

	node := newFakeNode()
	e := New(node, quiet())
	ctx := context.Background()
	e.SetServers(ctx, []Server{{Base: home.URL, Label: "home", Client: madshare.New(home.URL, "t")}})
	e.round(ctx)

	for _, st := range e.Status() {
		if st.Problem != "" {
			t.Fatalf("the round reported %q — this reproduction expects a clean round around a dead grant", st.Problem)
		}
	}
	if !e.Present(home.URL) {
		t.Fatal("Present refused the expired vouch — the expiry check exists now; " +
			"invert this test to pin it (and give the fetcher its honest decline)")
	}
	if got := node.snapshot().token; got != "expired-vouch" {
		t.Fatalf("installed token = %q, want the expired one on the wire (the defect this test pins)", got)
	}
}

// TestAFailedRenewalKeepsPresentingTheCorpse: the unreachable-server state.
// The server issues a short-lived grant, then goes away. Once the grant's life
// is over, the retrying round FAILS — the status says so, honestly — and yet
// Present keeps saying yes with the dead token, so fetches keep running with a
// vouch every holder refuses instead of declining with a reason a person could
// act on.
//
// FLIP the tail of this when the expiry check lands: after the expiry,
// Present must answer false however long the server stays gone.
func TestAFailedRenewalKeepsPresentingTheCorpse(t *testing.T) {
	home := newExpiryHome(t)
	home.issue(madshare.Grant{
		Token:      "short-vouch",
		Issuer:     "issuer-key",
		ExpiresAt:  time.Now().Add(600 * time.Millisecond),
		RenewAfter: time.Now(), // due immediately, so the next round retries
	})

	node := newFakeNode()
	e := New(node, quiet())
	ctx := context.Background()
	e.SetServers(ctx, []Server{{Base: home.URL, Label: "home", Client: madshare.New(home.URL, "t")}})
	e.round(ctx)
	if !e.Present(home.URL) {
		t.Fatal("the fresh vouch should present — the grant is still alive here")
	}

	// The server vanishes and the token's life runs out.
	home.setDown(true)
	time.Sleep(700 * time.Millisecond)
	e.round(ctx)

	problem := ""
	for _, st := range e.Status() {
		problem = st.Problem
	}
	if problem == "" {
		t.Fatal("the failed round should be reported — that half works and must keep working")
	}
	if !e.Present(home.URL) {
		t.Fatal("Present refused the dead vouch — the expiry check exists now; " +
			"invert this test to pin it")
	}
	if got := node.snapshot().token; got != "short-vouch" {
		t.Fatalf("installed token = %q, want the dead one on the wire (the defect this test pins)", got)
	}
}
