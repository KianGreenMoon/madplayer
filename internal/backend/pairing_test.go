package backend

import (
	"context"
	"testing"
)

// The pairing passthrough (pairing.go): the madshare surface reduced to rows
// the UI may hold. The friendship state machine is madshare's and tested
// there; this is about the translation and the mesh-off refusals.

func TestPairingRefusesWithoutTheMesh(t *testing.T) {
	be := openTestBackend(t, Options{})
	if _, ok := be.NodeIdentity(); ok {
		t.Error("NodeIdentity is available with the mesh off")
	}
	if _, err := be.Peers(context.Background()); err == nil {
		t.Error("Peers answered with the mesh off; want a refusal")
	}
	if _, err := be.PairWith(context.Background(), "0000"); err == nil {
		t.Error("PairWith answered with the mesh off; want a refusal")
	}
}

func TestPairingRoundTrip(t *testing.T) {
	be := openTestBackend(t, Options{Mesh: true})
	if why := be.MeshProblem(); why != "" {
		t.Skipf("no mesh on this host: %s", why)
	}

	id, ok := be.NodeIdentity()
	if !ok {
		t.Fatal("NodeIdentity not available with the mesh up")
	}
	if len(id.Key) != 64 || id.Card == "" {
		t.Fatalf("identity = %+v, want a 64-hex key and a card", id)
	}

	// A different, well-formed key: the own key with its first byte changed.
	other := "00" + id.Key[2:]
	if other == id.Key {
		other = "01" + id.Key[2:]
	}
	p, err := be.PairWith(context.Background(), other)
	if err != nil {
		t.Fatalf("PairWith: %v", err)
	}
	if p.State != "pending_outgoing" {
		t.Errorf("state after import = %q, want pending_outgoing", p.State)
	}

	peers, err := be.Peers(context.Background())
	if err != nil {
		t.Fatalf("Peers: %v", err)
	}
	if len(peers) != 1 || peers[0].Key != other {
		t.Fatalf("Peers = %+v, want exactly the imported key", peers)
	}
	if !peers[0].LastSeen.IsZero() {
		t.Errorf("LastSeen = %v for a never-contacted peer, want zero", peers[0].LastSeen)
	}

	if err := be.RemovePeer(context.Background(), peers[0].ID); err != nil {
		t.Fatalf("RemovePeer: %v", err)
	}
	if peers, _ = be.Peers(context.Background()); len(peers) != 0 {
		t.Fatalf("Peers after remove = %+v, want none", peers)
	}
}
