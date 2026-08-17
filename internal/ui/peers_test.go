package ui

import (
	"strings"
	"testing"
	"time"

	"daemonlord.ygg/madplayer/internal/backend"
)

// The peer list is the third way onto the mesh, and the one a person types by
// hand — so what matters is that a typo is refused with a sentence rather than
// saved and silently never dialled, and that what IS saved reaches the file
// main.go reads on the way up.

func TestPeerAddressesAreCheckedBeforeTheyAreSaved(t *testing.T) {
	good := []string{
		"tls://example.org:7743",
		"tcp://192.168.1.5:9001",
		"quic://[2001:db8::1]:443",
		"wss://mesh.example.org:443",
		"unix:///run/yggdrasil.sock",
	}
	for _, uri := range good {
		if _, err := checkPeer(uri); err != nil {
			t.Errorf("%s was refused: %v", uri, err)
		}
	}

	bad := []struct{ uri, why string }{
		{"", "nothing typed"},
		{"example.org:7743", "no scheme — the commonest way to get this wrong"},
		{"https://example.org", "a scheme yggdrasil cannot dial"},
		{"tls://example.org", "no port"},
		{"tls://", "no host"},
	}
	for _, c := range bad {
		if _, err := checkPeer(c.uri); err == nil {
			t.Errorf("%q was accepted (%s)", c.uri, c.why)
		}
	}
}

// Whitespace is what a pasted address carries, and it is not part of the
// address. The clipboard button trims too, but a peer can also be typed.
func TestAPeerAddressIsTrimmed(t *testing.T) {
	got, err := checkPeer("  tls://example.org:7743\n")
	if err != nil {
		t.Fatal(err)
	}
	if got != "tls://example.org:7743" {
		t.Errorf("kept %q", got)
	}
}

// A peer is worth nothing unless it survives the restart that dials it: the
// config file is what main.go hands the backend on the way up.
func TestAddingAPeerReachesTheConfigFile(t *testing.T) {
	a := testApp(t)
	a.addPeer("tls://example.org:7743")

	peers := waitForPeers(t, a, 1)
	if peers[0] != "tls://example.org:7743" {
		t.Fatalf("saved %q", peers[0])
	}
	// The box empties on success, which is what makes accepted and refused look
	// different at a glance.
	if a.peerEd.Text() != "" {
		t.Errorf("the box still holds %q after the peer was accepted", a.peerEd.Text())
	}

	// The same address twice is one peer, not two links to the same node.
	a.addPeer("tls://example.org:7743")
	a.mu.Lock()
	n := len(a.cfg.MeshPeers)
	msg := a.peerMsg
	a.mu.Unlock()
	if n != 1 {
		t.Errorf("adding the same peer twice left %d of them", n)
	}
	if !strings.Contains(msg, "already") {
		t.Errorf("the second add said %q", msg)
	}

	a.removePeer("tls://example.org:7743")
	if left := waitForPeers(t, a, 0); len(left) != 0 {
		t.Errorf("removing left %v in the file", left)
	}
}

// A refused address must not be written down at all: a config file carrying
// something yggdrasil cannot dial is a peer that fails silently at every start.
func TestARefusedPeerIsNotSaved(t *testing.T) {
	a := testApp(t)
	a.addPeer("example.org:7743")

	a.mu.Lock()
	peers, msg := a.cfg.MeshPeers, a.peerMsg
	a.mu.Unlock()
	if len(peers) != 0 {
		t.Errorf("an address with no protocol was saved: %v", peers)
	}
	if msg == "" {
		t.Error("it was refused without saying why")
	}
}

// waitForPeers waits for the background save, which happens off the UI
// goroutine exactly like every other write in this program.
func waitForPeers(t *testing.T, a *App, want int) []string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		saved, err := a.store.Load()
		if err == nil && len(saved.MeshPeers) == want {
			return saved.MeshPeers
		}
		if time.Now().After(deadline) {
			t.Fatalf("the peer list never reached the settings file (want %d, err %v)", want, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// What a peer row SAYS is the whole point of reaching into the mesh for it. The
// four states were indistinguishable before madshare's facade grew
// UnderlayPeers (v0.8.12): a wrong address, an unreachable one, one still
// trying and one working all read the same, because AddPeer returns nil for
// every one of them.
func TestAPeerRowSaysWhichOfTheFourStatesItIsIn(t *testing.T) {
	cases := []struct {
		name string
		live *backend.UnderlayPeer
		want string
		warn bool
	}{
		{
			name: "not dialled at all",
			live: nil,
			want: "madnetwork is off",
		},
		{
			name: "connected",
			live: &backend.UnderlayPeer{URI: "tls://a:1", Up: true, Latency: 12 * time.Millisecond, Uptime: 4 * time.Minute},
			want: "Connected",
		},
		{
			name: "refusing, with the reason",
			live: &backend.UnderlayPeer{URI: "tls://a:1", Problem: "connection refused", ProblemAge: 9 * time.Second},
			want: "connection refused",
			warn: true,
		},
		{
			name: "trying, nothing to report yet",
			live: &backend.UnderlayPeer{URI: "tls://a:1"},
			want: "Connecting",
		},
	}
	for _, c := range cases {
		got, warn := underlayStateText(c.live)
		if !strings.Contains(got, c.want) {
			t.Errorf("%s: %q does not say %q", c.name, got, c.want)
		}
		if warn != c.warn {
			t.Errorf("%s: warn=%v, want %v — only a failure earns the warning colour", c.name, warn, c.warn)
		}
	}

	// A connected peer's line carries the numbers somebody uses to judge it.
	up, _ := underlayStateText(&backend.UnderlayPeer{Up: true, Latency: 12 * time.Millisecond, Uptime: 4 * time.Minute})
	for _, want := range []string{"12ms", "up 4m"} {
		if !strings.Contains(up, want) {
			t.Errorf("a connected peer says %q, missing %q", up, want)
		}
	}
}

// A peer typed with a query — ?key=, ?password=, ?maxbackoff= — must still find
// its own state. Yggdrasil keys a link by the URI with the query stripped
// (urlForLinkInfo), so a naive match leaves such a peer permanently reading as
// "not dialled" while it is connected.
func TestAPeerWithAQueryStillFindsItsOwnState(t *testing.T) {
	live := []backend.UnderlayPeer{{URI: "tls://example.org:7743", Up: true}}

	if got := underlayFor(live, "tls://example.org:7743?key=abc123"); got == nil {
		t.Fatal("a peer typed with ?key= did not match its own peering")
	} else if !got.Up {
		t.Error("matched the wrong peering")
	}
	// And it must not then be listed a second time as somebody else's.
	if extra := undeclaredPeers(live, []string{"tls://example.org:7743?key=abc123"}); len(extra) != 0 {
		t.Errorf("the same peering was also listed as undeclared: %+v", extra)
	}
}

// Peerings this device never typed — a home server's published peer, a
// multicast neighbour, an inbound link — belong on the page: they are the
// answer to "the list is empty and the madnetwork says On, so what is it
// connected to?".
func TestPeeringsNobodyTypedAreStillShown(t *testing.T) {
	live := []backend.UnderlayPeer{
		{URI: "tls://typed.example:7743"},
		{URI: "tcp://[fe80::1]:41657", Up: true, Inbound: true},
	}
	extra := undeclaredPeers(live, []string{"tls://typed.example:7743"})
	if len(extra) != 1 || extra[0].URI != "tcp://[fe80::1]:41657" {
		t.Fatalf("undeclared peerings = %+v", extra)
	}
	if s, _ := underlayStateText(&extra[0]); !strings.Contains(s, "dialled this device") {
		t.Errorf("an inbound peering reads as %q, which does not say who dialled whom", s)
	}
}
