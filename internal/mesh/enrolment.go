// Package mesh keeps this device's standing with the servers it is signed in to.
//
// A madplayer is a listener node: it publishes no friend list and appears in
// nobody else's, so no graph walk can place it and nothing on the mesh would
// otherwise know it exists (docs/architecture/federation.md §"The household").
// Everything an ordinary node gets from being in a graph, this device has to ask
// its home servers for, and keep asking:
//
//	a vouch it cannot earn      POST /api/madnetwork/token
//	a way onto the underlay     GET  /api/madnetwork/peering
//	a way to be found           POST /api/madnetwork/holdings
//
// None of it is durable. A token expires in an hour, an advertisement goes stale
// in ninety minutes, and a peering is a live connection. So this is a loop rather
// than a step in signing in, and the loop is the feature: a device that stops
// asking quietly stops being part of anything.
package mesh

import (
	"context"
	"log"
	"os"
	"sync"
	"time"

	"daemonlord.ygg/madplayer/internal/madshare"
)

// Node is this device's own mesh surface — the embedded backend's app.Network,
// narrowed to what enrolment uses.
//
// Declared here rather than imported so that internal/backend stays the only
// package that touches madshare. Narrowing it also happens to say what this
// package is: none of these methods fetch anything, because enrolment is about
// standing rather than about content.
type Node interface {
	// Key is this device's node key — what a token names as its bearer.
	Key() string
	// SetToken installs the vouch presented on outbound mesh requests.
	SetToken(token string)
	// AddPeer dials an underlay peer now. Re-adding a known one is a no-op.
	AddPeer(uri string) error
	// AddHome records a server whose word this device will take about who a
	// stranger is, so that server and its other devices may fetch from here.
	AddHome(ctx context.Context, publicKey, baseURL, name string) error
	// RemoveHome forgets one, on signing out.
	RemoveHome(ctx context.Context, publicKey string) error
	// Holdings is what this device has fetched and would seed.
	Holdings() []string
}

// Server is one home server this device is signed in to.
type Server struct {
	Base   string
	Label  string
	Client *madshare.Client
}

// Status is what a settings screen shows about one server's enrolment.
type Status struct {
	Base string
	// Key is that server's node key, learned from the token it issued. Empty
	// until the first successful round.
	Key string
	// Enrolled is the last time this device got a vouch from it.
	Enrolled time.Time
	// Advertised is how many cache blobs it was last told about.
	Advertised int
	// Peers is how many underlay URIs it offered.
	Peers int
	// Problem is why the last round failed, or "" — a sentence, not an error
	// dump: a server being down is an ordinary thing for a phone.
	Problem string
}

// tick is how often the loop looks for work. It is not the cadence of anything —
// each server carries its own due time, from what the server itself said — but
// the granularity at which those are noticed. A minute is far below every
// deadline involved and costs nothing, since a round that is not due does no I/O.
const tick = time.Minute

// retry is how long to wait after a failed round before trying that server
// again. Deliberately much shorter than the deadlines it is defending: losing a
// token costs the mesh, and a phone's network comes and goes.
const retry = 2 * time.Minute

// Enrolment maintains that standing for every server.
type Enrolment struct {
	node Node
	name string
	log  *log.Logger

	mu      sync.Mutex
	servers map[string]*enrolled
	// presented is the base URL whose token is currently installed. One token
	// can be presented at a time — the header carries one — so a device signed
	// in to several servers picks per fetch; see Present.
	presented string

	wake chan struct{}
}

type enrolled struct {
	server Server
	status Status
	// due is when this server next needs a round: the earlier of the token's
	// renewal and the advertisement's refresh, because one round does both.
	due   time.Time
	token string
}

// New returns an enrolment for this device. name is what home servers will call
// it; the hostname is a better answer than a generated id, because the person
// reading it is looking at their own list of their own devices.
func New(node Node, lg *log.Logger) *Enrolment {
	if lg == nil {
		lg = log.Default()
	}
	name, err := os.Hostname()
	if err != nil || name == "" {
		name = "madplayer"
	}
	return &Enrolment{
		node:    node,
		name:    name,
		log:     lg,
		servers: map[string]*enrolled{},
		wake:    make(chan struct{}, 1),
	}
}

// SetServers replaces the list, keeping what is already known about the ones
// that stayed. A server that is gone is forgotten on the mesh too: this device
// stops taking its word about strangers, which is what signing out means from
// the mesh's side.
func (e *Enrolment) SetServers(ctx context.Context, servers []Server) {
	e.mu.Lock()
	next := make(map[string]*enrolled, len(servers))
	for _, s := range servers {
		if cur, ok := e.servers[s.Base]; ok {
			cur.server = s
			next[s.Base] = cur
			continue
		}
		next[s.Base] = &enrolled{server: s, status: Status{Base: s.Base}}
	}
	var dropped []*enrolled
	for base, cur := range e.servers {
		if _, kept := next[base]; !kept {
			dropped = append(dropped, cur)
		}
	}
	e.servers = next
	e.mu.Unlock()

	for _, d := range dropped {
		if d.status.Key == "" {
			continue
		}
		if err := e.node.RemoveHome(ctx, d.status.Key); err != nil {
			e.log.Printf("mesh: forget %s: %v", d.server.Base, err)
		}
	}
	e.nudge()
}

// Present installs the token issued by one server, and reports whether there was
// one.
//
// A device signed in to several servers holds several tokens, and the mesh
// carries ONE — the header has room for a single vouch, and a third node checks
// it against an issuer IT can place. So the right token is the one from the
// server that named the holders being fetched from, and the caller installs it
// immediately before the fetch. That is why mesh fetches are run one at a time.
func (e *Enrolment) Present(base string) bool {
	e.mu.Lock()
	cur, ok := e.servers[base]
	token := ""
	if ok {
		token = cur.token
	}
	if token != "" {
		e.presented = base
	}
	e.mu.Unlock()
	if token == "" {
		return false
	}
	e.node.SetToken(token)
	return true
}

// Status reports every server's enrolment, for a settings screen.
func (e *Enrolment) Status() []Status {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]Status, 0, len(e.servers))
	for _, cur := range e.servers {
		out = append(out, cur.status)
	}
	return out
}

// Run keeps every server's enrolment current until ctx ends.
func (e *Enrolment) Run(ctx context.Context) {
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		e.round(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-e.wake:
		}
	}
}

func (e *Enrolment) nudge() {
	select {
	case e.wake <- struct{}{}:
	default: // a round is already pending; one is enough
	}
}

// round enrols every server that is due.
func (e *Enrolment) round(ctx context.Context) {
	now := time.Now()
	e.mu.Lock()
	due := make([]*enrolled, 0, len(e.servers))
	for _, cur := range e.servers {
		if !cur.due.After(now) {
			due = append(due, cur)
		}
	}
	e.mu.Unlock()

	for _, cur := range due {
		if ctx.Err() != nil {
			return
		}
		e.enrol(ctx, cur)
	}
}

// enrol runs one server's round: get a vouch, get onto its mesh, say what we
// hold.
//
// The order matters. The token comes first because it is the only step whose
// failure means the others are pointless — without a vouch this device is a
// stranger to everything the server knows, and telling it what we hold would
// advertise something nobody may fetch.
func (e *Enrolment) enrol(ctx context.Context, cur *enrolled) {
	base := cur.server.Base
	status := Status{Base: base}

	grant, err := cur.server.Client.IssueToken(ctx, e.node.Key())
	if err != nil {
		e.fail(cur, "could not get a madnetwork pass from this server: "+err.Error())
		return
	}
	// The issuer field is this server's own node key: recording it is what lets
	// this device serve that server and its other devices back, and it arrives
	// with the token rather than costing a call of its own.
	if err := e.node.AddHome(ctx, grant.Issuer, base, cur.server.Label); err != nil {
		e.fail(cur, "could not record this server as a home node: "+err.Error())
		return
	}
	status.Key = grant.Issuer
	status.Enrolled = time.Now()
	next := renewalOf(grant)

	// Getting onto the underlay. A server that shares no peering is not a
	// problem to report: this device may already be on the mesh by multicast or
	// by a typed peer, and those are the other two ways in.
	if peering, err := cur.server.Client.Peering(ctx); err == nil {
		uris := peering.URIs()
		status.Peers = len(uris)
		for _, uri := range uris {
			if err := e.node.AddPeer(uri); err != nil {
				e.log.Printf("mesh: peer %s from %s: %v", uri, base, err)
			}
		}
	} else if !madshare.NotShared(err) {
		e.log.Printf("mesh: peering from %s: %v", base, err)
	}

	// And being findable. Failing this costs only the ability to seed, so it is
	// logged and does not fail the round — the device is still enrolled, and
	// still fetches.
	holdings := e.node.Holdings()
	if refresh, err := cur.server.Client.PushHoldings(ctx, e.node.Key(), e.name, holdings); err != nil {
		e.log.Printf("mesh: advertise %d blob(s) to %s: %v", len(holdings), base, err)
	} else {
		status.Advertised = len(holdings)
		if refresh > 0 && time.Now().Add(refresh).Before(next) {
			next = time.Now().Add(refresh)
		}
	}

	e.mu.Lock()
	cur.status = status
	cur.token = grant.Token
	cur.due = next
	presented := e.presented
	e.mu.Unlock()

	// Keep the installed vouch current: if this is the server whose token is on
	// the wire, the renewal has to reach the wire too, or the transfers running
	// right now start failing at the hour.
	if presented == base || presented == "" {
		e.Present(base)
	}
}

// fail records why a server's round did not work and schedules a retry. The
// token, if there is one, is left installed: it is valid until it expires, and a
// server being unreachable for a moment is not a reason to stop being able to
// fetch from anybody.
func (e *Enrolment) fail(cur *enrolled, why string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	cur.status.Problem = why
	cur.due = time.Now().Add(retry)
}

// renewalOf is when to come back for a fresh token: what the server said, or the
// half-life of what it gave us if it said nothing.
//
// Renewing at the half-life rather than at expiry is the server's own rule, and
// the reason is a phone's: a transient outage should cost a retry rather than an
// interruption.
func renewalOf(g madshare.Grant) time.Time {
	if !g.RenewAfter.IsZero() {
		return g.RenewAfter
	}
	if !g.ExpiresAt.IsZero() {
		return time.Now().Add(time.Until(g.ExpiresAt) / 2)
	}
	return time.Now().Add(30 * time.Minute)
}
