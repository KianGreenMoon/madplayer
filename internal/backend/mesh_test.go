package backend

import (
	"context"
	"errors"
	"io"
	"log"
	"os/exec"
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
	cfg, why := playerConfig(t.TempDir(), Options{})
	if cfg.Federation.Enabled || cfg.Yggdrasil.Multicast {
		t.Error("a player joins the madnetwork without being asked")
	}
	if why != "" {
		t.Errorf("mesh problem = %q, want none — nobody asked for it", why)
	}
}

// TestPlayerConfigNeedsFpcalc is the shape decision behind the whole switch.
// madshare refuses to START a federated node without fpcalc, which is right for
// a server and wrong for a music player: the app must open either way, and the
// person must be told what to install rather than left with a switch that does
// nothing.
func TestPlayerConfigNeedsFpcalc(t *testing.T) {
	cfg, why := playerConfig(t.TempDir(), Options{Mesh: true, Peers: []string{"tls://a.example:1"}})
	if _, err := exec.LookPath("fpcalc"); err != nil {
		if cfg.Federation.Enabled {
			t.Error("the mesh was enabled without fpcalc; madshare would refuse to start")
		}
		if !strings.Contains(why, "fpcalc") {
			t.Errorf("mesh problem = %q, want it to name fpcalc", why)
		}
		return
	}
	if !cfg.Federation.Enabled || why != "" {
		t.Fatalf("mesh off with fpcalc present: enabled=%v why=%q", cfg.Federation.Enabled, why)
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
	// not being reachable over HTTP.
	if len(cfg.Listen) != 0 {
		t.Errorf("listeners = %v, want none, ever", cfg.Listen)
	}
}

// TestOpenWithMeshStartsOrExplains: whichever way this host is equipped, opening
// the backend with the mesh requested must succeed. The difference is whether
// there is a node afterwards or a sentence saying why not.
func TestOpenWithMeshStartsOrExplains(t *testing.T) {
	be := openTestBackend(t, Options{Mesh: true})
	net, up := be.Mesh()
	if _, err := exec.LookPath("fpcalc"); err != nil {
		if up {
			t.Error("the mesh is up without fpcalc")
		}
		if be.MeshProblem() == "" {
			t.Error("the mesh is off and nothing says why")
		}
		return
	}
	if !up {
		t.Fatalf("the mesh is off with fpcalc present: %s", be.MeshProblem())
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
