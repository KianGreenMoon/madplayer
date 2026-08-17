package backend

// The pairing test surface: this device's node befriending a server by
// exchanged public keys, the way two madshare servers do — madshare's
// EXPERIMENTAL app.Pairing (v0.8.11), translated into rows the UI may hold.
//
// A device paired this way is a full member of the community: a gossiped
// edge, a place on the network map, holders of its own — everything a server
// gets, nothing withheld. The quiet listener path (federation-access.md
// §"The household") remains what a device that does NOT pair gets; pairing is
// how this one opts into membership instead. Experimental while the owner
// tries it (2026-08-17): switched by ui.pairingEnabled, and removing it
// entirely is deleting this file and internal/ui/pairing.go. Sharing is a
// separate axis and unchanged — the library stays pinned closed, only the
// seeded cache is served.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// NodeIdentity is this node's own half of a pairing: what the other side's
// admin needs to accept us, in both the forms madshare accepts.
type NodeIdentity struct {
	Name    string
	Key     string // lowercase hex ed25519
	Address string // derived mesh address, for display
	Card    string // the copy-paste node card JSON
}

// Peer is one row of the node's trusted-peer table.
type Peer struct {
	ID    int64
	Key   string
	Name  string // the admin's label, or what the node calls itself, or ""
	State string // pending_outgoing · pending_incoming · friend · blocked
	// LastSeen is the last successful contact; zero = never.
	LastSeen time.Time
}

// NodeIdentity reports this node's own card, or false when the mesh is not
// running.
func (b *Backend) NodeIdentity() (NodeIdentity, bool) {
	p, ok := b.inst.Pairing()
	if !ok {
		return NodeIdentity{}, false
	}
	info := p.Info()
	card, err := json.Marshal(info.Card)
	if err != nil {
		return NodeIdentity{}, false
	}
	return NodeIdentity{
		Name:    info.Name,
		Key:     info.PublicKey,
		Address: info.Address,
		Card:    string(card),
	}, true
}

// Peers lists the node's trusted-peer table, friends first.
func (b *Backend) Peers(ctx context.Context) ([]Peer, error) {
	p, ok := b.inst.Pairing()
	if !ok {
		return nil, errors.New("the madnetwork is not running")
	}
	rows, err := p.Peers(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Peer, 0, len(rows))
	for _, r := range rows {
		name := r.Label
		if name == "" {
			name = r.HeardName
		}
		var seen time.Time
		if r.LastSeen > 0 {
			seen = time.Unix(r.LastSeen, 0)
		}
		out = append(out, Peer{ID: r.ID, Key: r.PublicKey, Name: name, State: r.TrustState, LastSeen: seen})
	}
	return out, nil
}

// PairWith imports the other side in whichever form was pasted: a node card
// (JSON) or a bare public key. A new node becomes pending_outgoing and is
// contacted immediately; for a node that already asked, this completes the
// friendship.
func (b *Backend) PairWith(ctx context.Context, input string) (Peer, error) {
	p, ok := b.inst.Pairing()
	if !ok {
		return Peer{}, errors.New("the madnetwork is not running")
	}
	input = strings.TrimSpace(input)
	if input == "" {
		return Peer{}, errors.New("paste a node card or a public key first")
	}
	if strings.HasPrefix(input, "{") {
		r, err := p.ImportCard(ctx, []byte(input))
		if err != nil {
			return Peer{}, err
		}
		return Peer{ID: r.ID, Key: r.PublicKey, Name: r.HeardName, State: r.TrustState}, nil
	}
	r, err := p.ImportKey(ctx, input, "")
	if err != nil {
		return Peer{}, err
	}
	return Peer{ID: r.ID, Key: r.PublicKey, Name: r.HeardName, State: r.TrustState}, nil
}

// AcceptPeer answers a node that asked to pair with us.
func (b *Backend) AcceptPeer(ctx context.Context, id int64) error {
	p, ok := b.inst.Pairing()
	if !ok {
		return errors.New("the madnetwork is not running")
	}
	return p.AcceptPeer(ctx, id)
}

// RemovePeer forgets a peer row entirely.
func (b *Backend) RemovePeer(ctx context.Context, id int64) error {
	p, ok := b.inst.Pairing()
	if !ok {
		return errors.New("the madnetwork is not running")
	}
	return p.RemovePeer(ctx, id)
}
