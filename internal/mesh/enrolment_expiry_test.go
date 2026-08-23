package mesh

// What enrolment does about a vouch whose life is over.
//
// These pin the fix for the defect behind "the madnetwork stopped playing
// after the laptop woke up" (found 2026-08-23, reproduced end to end in
// internal/backend/labnet_test.go): the enrolment used to record a grant's
// token and forget its ExpiresAt. Present answered "is there a vouch" from the
// token's mere existence, so a token that died hours ago was still installed
// on the wire, every holder refused it — deliberately without saying why — and
// a madnetwork track failed looking like nobody holds it. The honest decline
// the fetcher carries ("this device has no vouch from X yet", remote/fetch.go)
// was unreachable, because Present never said no once it had ever said yes.
//
// Two ways a device gets there, both ordinary:
//
//   - the machine sleeps. The loop's tick, the due times and every time.Time
//     comparison here ride the MONOTONIC clock, which stands still during a
//     suspend on Linux (and an app freeze on Android) — so after eight hours
//     asleep the loop still believes renewal is half an hour away, while the
//     wall clock every verifier reads moved eight hours.
//   - the home server is unreachable past the token's life. fail() keeps the
//     old token on purpose ("valid until it expires" — true, and now checked),
//     so once the hour passes the kept token is a corpse.
//
// The fix keeps the grant's ExpiresAt (JSON-decoded, so it carries no
// monotonic reading — comparing time.Now() against it reads the wall clock,
// suspend-proof), makes Present refuse a dead token and mark its server due,
// and makes the round treat a wall-clock-dead token as due whatever the
// monotonic due time says. These tests began life asserting the broken
// behaviour and were inverted when the fix landed; each now names the
// assertion that holds.

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

// TestPresentRefusesAVouchThatHasExpired: the post-sleep state. The grant
// enrolment holds is wall-clock dead while its RenewAfter — the only date the
// loop used to pace itself by — says "not due yet". Present must answer no and
// keep the corpse off the wire (the fetcher then declines with its honest "no
// vouch from X yet" instead of burning the swarm budget on refusals), and the
// refusal must mark the server due — so the next round, not RenewAfter's
// half-hour, brings the fresh token.
func TestPresentRefusesAVouchThatHasExpired(t *testing.T) {
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

	// The round itself is clean — the server answered everything it was asked.
	// The dates on the answer are the problem, and Present is the gate.
	for _, st := range e.Status() {
		if st.Problem != "" {
			t.Fatalf("the round reported %q — a dead grant is refused at Present, not reported as a round failure", st.Problem)
		}
	}
	if e.Present(home.URL) {
		t.Fatal("Present offered a vouch that expired half an hour ago — every holder would refuse it without saying why")
	}
	if got := node.snapshot().token; got == "expired-vouch" {
		t.Fatal("the expired token reached the wire anyway")
	}

	// The refusal marks the server due, so the server being healthy is enough:
	// the very next round — the one the nudge wakes — gets a fresh vouch, and
	// a fetch a few seconds after the refusal succeeds.
	home.issue(madshare.Grant{
		Token:      "fresh-vouch",
		Issuer:     "issuer-key",
		ExpiresAt:  time.Now().Add(time.Hour),
		RenewAfter: time.Now().Add(30 * time.Minute),
	})
	e.round(ctx)
	if !e.Present(home.URL) {
		t.Fatal("Present still refuses after a healthy renewal round")
	}
	if got := node.snapshot().token; got != "fresh-vouch" {
		t.Fatalf("installed token = %q, want the renewed one", got)
	}
}

// TestTheRoundRenewsAWallClockDeadTokenBeforeItsDueTime: the suspend shape at
// round level, with no Present in between to do the nudging. The kept grant's
// due time (RenewAfter) is far in the future — the monotonic clock slept
// through the token's life — so the old dueness check would wait half an hour.
// The round must notice the wall-clock death itself and re-enrol now.
func TestTheRoundRenewsAWallClockDeadTokenBeforeItsDueTime(t *testing.T) {
	home := newExpiryHome(t)
	home.issue(madshare.Grant{
		Token:      "slept-through",
		Issuer:     "issuer-key",
		ExpiresAt:  time.Now().Add(-8 * time.Hour),
		RenewAfter: time.Now().Add(30 * time.Minute),
	})

	node := newFakeNode()
	e := New(node, quiet())
	ctx := context.Background()
	e.SetServers(ctx, []Server{{Base: home.URL, Label: "home", Client: madshare.New(home.URL, "t")}})
	e.round(ctx)

	// Force the exact post-suspend shape. The round above already tripped
	// Present's own refusal (enrol keeps the wire current), which marks the
	// server due — but the state a real suspend leaves behind is one where
	// NOTHING has run since: a dead token under a due time half an hour out.
	// Build that state directly, so what this test exercises is the round's
	// own wall-clock check and not Present's nudge.
	e.mu.Lock()
	for _, cur := range e.servers {
		cur.due = time.Now().Add(30 * time.Minute)
	}
	e.mu.Unlock()

	// The machine "wakes": the server is fine and issues living grants again.
	home.issue(madshare.Grant{
		Token:      "morning-vouch",
		Issuer:     "issuer-key",
		ExpiresAt:  time.Now().Add(time.Hour),
		RenewAfter: time.Now().Add(30 * time.Minute),
	})
	e.round(ctx)

	if !e.Present(home.URL) {
		t.Fatal("Present has no living vouch — the round waited out a due time whose clock stood still")
	}
	if got := node.snapshot().token; got != "morning-vouch" {
		t.Fatalf("installed token = %q, want the post-wake renewal", got)
	}
}

// TestAFailedRenewalStopsPresentingTheCorpse: the unreachable-server state.
// The server issues a short-lived grant, then goes away. Once the grant's life
// is over, the retrying round FAILS — the status says so, honestly, and that
// half must keep working — and Present must answer no however long the server
// stays gone, taking the dead token off the wire it put it on. fail() keeping
// the token itself is deliberate and stays: a token still ALIVE survives a
// failed round; only one past expiry stops being presented.
func TestAFailedRenewalStopsPresentingTheCorpse(t *testing.T) {
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
	if e.Present(home.URL) {
		t.Fatal("Present offered the dead vouch a failed renewal left behind")
	}
	if got := node.snapshot().token; got == "short-vouch" {
		t.Fatal("the dead token is still on the wire — refusing it must also take it down")
	}
}
