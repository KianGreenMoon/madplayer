package backend

// Node mode's data half (madshare docs/plans/full-node-mode.md; the UI half is
// internal/ui/nodemode.go): browsing the community through this device's OWN
// node, and choosing what this device shares back.
//
// Browsing through the own node is what pairing buys. A listener browses the
// madnetwork through the servers it is signed in to; a paired member pulls the
// community's catalogs itself, and madshare's app.Madnetwork() is the view
// over them — the same code the web UI's /madnetwork runs, which is the whole
// reason the facade exists (embedding.md §"The madnetwork browse and the
// publish picker").
//
// Sharing is the other half of the same bargain. This device's node default
// stays pinned Local (PublishNothing, backend.go) — nothing is shared until
// chosen, per item, in full knowledge. These wrappers are that choice.

import (
	"context"
	"errors"

	"daemonlord.ygg/madshare/app"
	"daemonlord.ygg/madshare/database"
	"daemonlord.ygg/madshare/federation"
)

// Community is the merged madnetwork browse over this device's own node, or
// false when the mesh is not running. The library package consumes it directly
// (library.SetNode) — app.Madnetwork is facade vocabulary, like app.Library.
func (b *Backend) Community() (app.Madnetwork, bool) {
	return b.inst.Madnetwork()
}

// CommunityHolders is the fetch plan for one blob, asked of this device's own
// node — the paired twin of a home server's /holders endpoint, with the same
// stale-holder freshness applied. Empty = nobody reachable holds it.
func (b *Backend) CommunityHolders(ctx context.Context, hash string) (int64, []string, error) {
	mn, ok := b.inst.Madnetwork()
	if !ok {
		return 0, nil, errors.New("the madnetwork is not running")
	}
	return mn.Holders(ctx, hash)
}

// ShareScope is how far a track travels, in the client's vocabulary. The three
// values map onto madshare's share depths; "not shared" is the inherited node
// default, which this backend pins to Local — so clearing a pin IS stopping
// the sharing, and the two never disagree.
type ShareScope int

const (
	// ShareNotShared: the track stays on this device (the default).
	ShareNotShared ShareScope = iota
	// ShareFriends: nodes this device paired with directly.
	ShareFriends
	// ShareMadnetwork: the whole community this device can reach.
	ShareMadnetwork
)

// SetShareScope pins the sharing scope of the given appearances (tagset ids —
// what device browse rows carry as their Origin.ID). ShareNotShared clears the
// pin back to the node default, which on this device means Local.
func (b *Backend) SetShareScope(ctx context.Context, tagsetIDs []int64, s ShareScope) error {
	u := database.ShareDepthUpdate{Set: true}
	switch s {
	case ShareFriends:
		u.Depth = federation.DepthFriends
	case ShareMadnetwork:
		u.Depth = federation.DepthUnlimited
	default:
		u.Inherit = true
	}
	return b.inst.SetShareDepth(ctx, tagsetIDs, u)
}

// ShareScopes reports each appearance's scope. Every id asked about is in the
// answer — an unpinned row (and a row pinned Local, which shares nothing
// either) answers ShareNotShared — so a picker can render rows without
// re-deriving the absence rule.
func (b *Backend) ShareScopes(ctx context.Context, tagsetIDs []int64) (map[int64]ShareScope, error) {
	depths, err := b.inst.ShareDepths(ctx, tagsetIDs)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]ShareScope, len(tagsetIDs))
	for _, id := range tagsetIDs {
		// Presence first: an absent id inherits the (Local) default, and
		// DepthFriends is the ZERO value — reading the map bare would report
		// every unpinned track as shared with friends.
		depth, pinned := depths[id]
		if !pinned {
			out[id] = ShareNotShared
			continue
		}
		switch depth {
		case federation.DepthFriends:
			out[id] = ShareFriends
		case federation.DepthUnlimited:
			out[id] = ShareMadnetwork
		default:
			out[id] = ShareNotShared
		}
	}
	return out, nil
}

// SharedTrack is one row of "what does the network see from this device".
type SharedTrack struct {
	TagsetID int64
	Title    string
	Artist   string
	Album    string
	Scope    ShareScope
}

// Published lists exactly what this device currently shares, in reading order.
// On this backend every row is there by a pin of its own — the node default
// publishes nothing — so removing rows here empties the catalog.
func (b *Backend) Published(ctx context.Context) ([]SharedTrack, error) {
	rows, err := b.inst.Published(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]SharedTrack, 0, len(rows))
	for _, r := range rows {
		s := SharedTrack{TagsetID: r.TagsetID, Title: r.Title, Artist: r.Artist, Album: r.Album}
		if r.Depth >= federation.DepthUnlimited {
			s.Scope = ShareMadnetwork
		} else if r.Depth >= federation.DepthFriends {
			s.Scope = ShareFriends
		}
		out = append(out, s)
	}
	return out, nil
}
