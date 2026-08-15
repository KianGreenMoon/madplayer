package backend

import (
	"context"
	"errors"
	"io"
	"log"
	"strings"
	"testing"
	"time"

	"daemonlord.ygg/madshare/app"
)

// openTestBackend is openBackend with the mesh switch under test.
func openTestBackend(t *testing.T, opts Options) *Backend {
	t.Helper()
	be, err := Open(context.Background(), t.TempDir(), log.New(io.Discard, "", 0), opts)
	if err != nil {
		t.Fatalf("open backend: %v", err)
	}
	t.Cleanup(be.Close)
	return be
}

// Becoming a node (docs/ui/madplayer.md §"Level 2b"). The mesh is the one part
// of this backend that is optional, so these are about the switch: what it turns
// on, and what happens when it cannot be honoured.

func TestPlayerConfigLeavesTheMeshOffByDefault(t *testing.T) {
	cfg := playerConfig(t.TempDir(), Options{})
	if cfg.Federation.Enabled || cfg.Yggdrasil.Multicast {
		t.Error("a player joins the madnetwork without being asked")
	}
}

// The switch turns the mesh on, and since 2026-08-15 nothing about this host
// can stop it. It used to depend on fpcalc being installed, which no phone can
// arrange; the fingerprinting is now this program's own (internal/chroma), so
// the requirement is met rather than waived.
func TestPlayerConfigEnablesTheMesh(t *testing.T) {
	cfg := playerConfig(t.TempDir(), Options{Mesh: true, Peers: []string{"tls://a.example:1"}})
	if !cfg.Federation.Enabled {
		t.Fatal("the mesh switch is on and federation is not")
	}
	// The load-bearing negative. madshare refuses to federate a node that cannot
	// fingerprint, and this key is how a node says "federate me anyway". Setting
	// it would make the switch work on Android by giving up the check that makes
	// seeding safe — which is the wrong way to solve this and always was.
	if cfg.Federation.AllowMissingFingerprinting {
		t.Error("allow_missing_fingerprinting is set: the mesh must satisfy the fingerprint gate, never bypass it")
	}
	if !cfg.Yggdrasil.Multicast {
		t.Error("multicast off — a phone finding its home server over the wifi is the case this exists for")
	}
	if len(cfg.Yggdrasil.Peers) != 1 || cfg.Yggdrasil.Peers[0] != "tls://a.example:1" {
		t.Errorf("peers = %v, want the typed one", cfg.Yggdrasil.Peers)
	}
	// A device serves no HTTP at all, so it has nothing to share peering FROM.
	if cfg.Yggdrasil.SharePeers == nil || *cfg.Yggdrasil.SharePeers {
		t.Error("share_peers left on — this program has no listener to serve it")
	}
	// And the listener-less rule still holds with the mesh on: being a node is
	// not being a server.
	if len(cfg.Listen) != 0 {
		t.Errorf("listeners = %v, want none even as a node", cfg.Listen)
	}
}

// TestOpenWithMeshStartsOnAnyHost: asking for the mesh gets a node, on whatever
// this machine happens to have installed.
//
// It used to be TestOpenWithMeshStartsOrExplains, and it branched on whether
// fpcalc was on PATH — which quietly meant the test asserted nothing on a host
// without it, including every phone. There is no branch left to take: madshare
// gates federation on fingerprinting and this program fingerprints itself.
func TestOpenWithMeshStartsOnAnyHost(t *testing.T) {
	be := openTestBackend(t, Options{Mesh: true})
	net, up := be.Mesh()
	if !up {
		t.Fatalf("the mesh is off: %s", be.MeshProblem())
	}
	if be.MeshProblem() != "" {
		t.Errorf("mesh problem = %q while the mesh runs", be.MeshProblem())
	}
	if len(net.Key()) != 64 {
		t.Errorf("node key = %q, want 64 hex characters", net.Key())
	}
}

// A device that is not a node says so rather than hanging. The fetcher installs
// the swarm only when Mesh reports it up, so this is the facade defending itself
// against a caller that did not check — and the failure it must not have is
// blocking forever on a transfer nobody started, which is a music player that
// stops playing.
func TestFetchBlobWithNoMeshFails(t *testing.T) {
	be := openTestBackend(t, Options{})
	done := make(chan error, 1)
	go func() {
		_, err := be.FetchBlob(context.Background(), strings.Repeat("a", 64), 0, []string{"bb22"})
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, app.ErrNoMesh) {
			t.Fatalf("FetchBlob without a mesh = %v, want ErrNoMesh", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("FetchBlob blocked on a device with no madnetwork node")
	}
}
