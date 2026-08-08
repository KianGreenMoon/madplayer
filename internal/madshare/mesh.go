package madshare

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// The four calls a device makes to its home server that are about the MESH
// rather than about the library (docs/architecture/federation.md
// §"The household").
//
// They are the substitutes for everything an ordinary node gets from a graph it
// is in: a vouch it cannot earn, a way onto the underlay, a way to be found, and
// a way to find. All four are ordinary authenticated calls on the same
// permission as browsing — a madplayer participates in madnetwork, it just does
// its own fetching.

// Grant is the response of POST /api/madnetwork/token: this server's signed
// statement that one node key belongs to one of its users.
//
// Issuer is worth as much as Token here. It is this server's own node key, which
// is how a device learns who its home server IS on the mesh without a second
// call — and that key is what the device records so it can serve that server and
// its other devices back.
type Grant struct {
	Token      string    `json:"token"`
	Issuer     string    `json:"issuer"`
	Bearer     string    `json:"bearer"`
	ExpiresAt  time.Time `json:"expires_at"`
	RenewAfter time.Time `json:"renew_after"`
}

// IssueToken asks this server to vouch for a device key.
//
// No card, no accept, no peer row: issuing stores nothing, because the token
// verifies from its own bytes. A device may therefore ask as often as it likes,
// which is what makes renewing at the half-life free.
func (c *Client) IssueToken(ctx context.Context, nodeKey string) (Grant, error) {
	var g Grant
	err := c.do(ctx, "POST", "/api/madnetwork/token", map[string]string{"node_key": nodeKey}, &g)
	return g, err
}

// Peering is the response of GET /api/madnetwork/peering: how to reach the mesh
// this server is on.
//
// Both lists are underlay URIs to dial. Listen are this server's own, already
// rewritten by it to a host the caller can actually reach; Peers are the ones it
// dials itself, shared so a device ends up on the same network rather than
// merely able to reach one machine.
type Peering struct {
	Peers  []string `json:"peers"`
	Listen []string `json:"listen"`
}

// URIs is everything worth dialling, listeners first.
//
// The server's own listener is the better bet and goes first: it is one hop, it
// is a machine this person already reaches, and it is the only entry that was
// checked against the address they used. Its shared peers follow as the wider
// way in.
func (p Peering) URIs() []string {
	out := make([]string, 0, len(p.Listen)+len(p.Peers))
	seen := map[string]bool{}
	for _, list := range [][]string{p.Listen, p.Peers} {
		for _, uri := range list {
			if uri == "" || seen[uri] {
				continue
			}
			seen[uri] = true
			out = append(out, uri)
		}
	}
	return out
}

// Peering asks how to get onto this server's mesh.
//
// A 404 is a real answer and not a failure: this operator switched sharing off.
// The caller has two other ways onto the mesh and should use them rather than
// treat the server as broken, so the error is returned as-is for the caller to
// classify with NotShared.
func (c *Client) Peering(ctx context.Context) (Peering, error) {
	var p Peering
	err := c.get(ctx, "/api/madnetwork/peering", &p)
	return p, err
}

// NotShared reports the one refusal of Peering that means "this is how it is"
// rather than "something went wrong": the server does not publish its peering.
func NotShared(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Status == http.StatusNotFound
}

// PushHoldings tells this server what the device's cache holds, so the server —
// and the other devices it vouches for — can fetch from it.
//
// A complete statement each time, replacing whatever was said before: an empty
// list is a swept cache and must stop the device being offered. The returned
// duration is the server's own cadence; pushing at least that often is what
// keeps the advertisement alive, and there is nothing else to keep it alive
// with.
func (c *Client) PushHoldings(ctx context.Context, nodeKey, name string, hashes []string) (time.Duration, error) {
	if hashes == nil {
		// An explicit empty array, never a JSON null: the two mean the same thing
		// to the server, but only one of them says so on purpose.
		hashes = []string{}
	}
	var out struct {
		RefreshAfter int64 `json:"refresh_after"`
	}
	err := c.do(ctx, "POST", "/api/madnetwork/holdings", map[string]any{
		"node_key": nodeKey,
		"name":     name,
		"hashes":   hashes,
	}, &out)
	if err != nil {
		return 0, err
	}
	return time.Duration(out.RefreshAfter) * time.Second, nil
}

// Holders is the response of GET /api/madnetwork/holders/{hash}: a fetch plan.
type Holders struct {
	Hash    string `json:"hash"`
	Size    int64  `json:"size"`
	Holders []struct {
		Key      string `json:"key"`
		Name     string `json:"name"`
		LastSeen int64  `json:"last_seen"`
	} `json:"holders"`
}

// Keys is the holder list in the form a fetch wants.
func (h Holders) Keys() []string {
	out := make([]string, 0, len(h.Holders))
	for _, x := range h.Holders {
		if x.Key != "" {
			out = append(out, x.Key)
		}
	}
	return out
}

// Holders asks who holds one blob.
//
// An empty list is a normal answer, not a 404: nobody reachable holds it, and
// the caller's fallback is this server's own streaming relay, which works
// whether or not anybody else is up.
func (c *Client) Holders(ctx context.Context, hash string) (Holders, error) {
	var h Holders
	err := c.get(ctx, "/api/madnetwork/holders/"+url.PathEscape(hash), &h)
	return h, err
}

// StreamURL is the relay this server runs on a device's behalf: the fallback
// when the swarm cannot deliver, and the whole of level 1's network playback.
func (c *Client) StreamURL(hash string) string {
	return fmt.Sprintf("%s/api/madnetwork/stream/%s", c.Base, url.PathEscape(hash))
}
