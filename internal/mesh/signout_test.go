package mesh

import (
	"context"
	"testing"
)

// Signing out of the server whose token is on the wire takes the token off the
// wire; signing out of any other server leaves it alone. Hygiene rather than
// security — the token expires within the hour and every fetch presents its
// own server's first — but a credential from a server this device left has no
// business staying the standing default (.issues/open-issues.md, 2026-08-15).
func TestSigningOutOfThePresentedServerClearsTheToken(t *testing.T) {
	node := newFakeNode()
	one, two := newHomeServer(t, "1111"), newHomeServer(t, "2222")
	e := New(node, quiet())
	ctx := context.Background()
	e.SetServers(ctx, []Server{one.server("one"), two.server("two")})
	e.round(ctx)

	if !e.Present(one.URL) {
		t.Fatal("Present reported no token for an enrolled server")
	}

	// Dropping the OTHER server leaves the presented vouch alone.
	e.SetServers(ctx, []Server{one.server("one")})
	if got := node.snapshot(); got.token != "token-for-1111" {
		t.Fatalf("token after dropping another server = %q, want the presented one kept", got.token)
	}

	// Dropping the presented one takes it off the wire.
	e.SetServers(ctx, nil)
	if got := node.snapshot(); got.token != "" {
		t.Fatalf("token after signing out of the presented server = %q, want cleared", got.token)
	}
}
