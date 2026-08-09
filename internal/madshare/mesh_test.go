package madshare

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"
)

// The mesh calls a device makes to its home server. The server's side is tested
// there; what matters here is the shape this client hands the rest of the
// program — and the one refusal it must not read as a failure.

// TestPeeringURIsPrefersTheServerItself: the server's own listener is one hop to
// a machine this person already reaches, and it is the only entry the server
// checked against the address they used. Its wider peers follow.
func TestPeeringURIsPrefersTheServerItself(t *testing.T) {
	p := Peering{
		Peers:  []string{"tls://upstream.example:1", "tls://box.local:12345"},
		Listen: []string{"tls://box.local:12345", ""},
	}
	got := p.URIs()
	want := []string{"tls://box.local:12345", "tls://upstream.example:1"}
	if len(got) != len(want) {
		t.Fatalf("URIs() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("URIs()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestNotSharedIsAnAnswer: a 404 from the peering endpoint means this operator
// switched sharing off. A device has two other ways onto the mesh and should use
// them, rather than treating the server as broken.
func TestNotSharedIsAnAnswer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "this node does not share its peering", http.StatusNotFound)
	}))
	defer srv.Close()

	_, err := New(srv.URL, "tok").Peering(context.Background())
	if err == nil {
		t.Fatal("Peering() succeeded against a server that refuses")
	}
	if !NotShared(err) {
		t.Errorf("NotShared(%v) = false, want true", err)
	}
	// And a real failure is not mistaken for that answer.
	if NotShared(context.Canceled) {
		t.Error("NotShared() true for an unrelated error")
	}
}

// TestPushHoldingsSendsAnEmptyListNotNull: an empty cache is a statement — it
// must stop this device being offered — and JSON null is not the way to make it.
func TestPushHoldingsSendsAnEmptyListNotNull(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "refresh_after": 2700})
	}))
	defer srv.Close()

	after, err := New(srv.URL, "tok").PushHoldings(context.Background(), "abcd", "phone", nil)
	if err != nil {
		t.Fatalf("PushHoldings: %v", err)
	}
	if after.Seconds() != 2700 {
		t.Errorf("refresh after = %v, want the server's own cadence", after)
	}
	hashes, ok := body["hashes"].([]any)
	if !ok || hashes == nil {
		t.Errorf("hashes = %#v, want an empty array", body["hashes"])
	}
	if len(hashes) != 0 {
		t.Errorf("hashes = %v, want empty", hashes)
	}
}

// TestHoldersKeepsStaleHoldersInTheServersOrder pins a decision that cost real
// time to find, measured 2026-08-09: the client does NOT second-guess the fetch
// plan, and the plan can contain nodes that have been gone for days.
//
// The server sends holders freshest-first and applies no staleness cutoff on its
// catalog branch. Against a live server that meant being handed nodes last seen
// 21 and 54 hours earlier, and each dead one costs the swarm four stall timeouts
// before it is retired — about ninety seconds of the four minutes those fetches
// took.
//
// Keys() therefore preserves the order exactly, because that order is the only
// thing standing between a fetch and dialling the corpse first. Filtering here
// was considered and rejected: which nodes are worth dialling is the server's
// call (it is the one with the graph), and a client that re-derives it quietly
// disagrees with every other client. When a cutoff lands it belongs in
// MadnetworkBlobProviders, and this test should keep passing unchanged.
func TestHoldersKeepsStaleHoldersInTheServersOrder(t *testing.T) {
	now := time.Now().Unix()
	body := fmt.Sprintf(`{"hash":"abc","size":17428924,"holders":[
		{"key":"live","name":"mainframe","last_seen":%d},
		{"key":"gone","name":"fedora","last_seen":%d},
		{"key":"ancient","name":"old test node","last_seen":%d}]}`,
		now-20, now-21*3600, now-54*3600)

	var h Holders
	if err := json.Unmarshal([]byte(body), &h); err != nil {
		t.Fatal(err)
	}
	if h.Size != 17428924 {
		t.Errorf("size = %d, want the advertised total", h.Size)
	}
	want := []string{"live", "gone", "ancient"}
	if got := h.Keys(); !slices.Equal(got, want) {
		t.Errorf("Keys() = %v, want %v — the server's order, stale entries and all", got, want)
	}
	// last_seen is decoded rather than dropped: nothing acts on it today, and a
	// client-side cutoff would start here if the server's one never arrives.
	if h.Holders[2].LastSeen != now-54*3600 {
		t.Errorf("last_seen = %d, want it carried through", h.Holders[2].LastSeen)
	}
}

// TestHoldersKeys drops what cannot be dialled, since a fetch plan is a list of
// addresses and nothing else.
func TestHoldersKeys(t *testing.T) {
	h := Holders{Holders: []struct {
		Key      string `json:"key"`
		Name     string `json:"name"`
		LastSeen int64  `json:"last_seen"`
	}{{Key: "aa"}, {Key: ""}, {Key: "bb"}}}
	got := h.Keys()
	if len(got) != 2 || got[0] != "aa" || got[1] != "bb" {
		t.Errorf("Keys() = %v, want the two keyed holders", got)
	}
}
