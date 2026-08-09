// Command meshcheck fetches one track the way the player does, against a real
// server, and says which path the bytes took.
//
// It exists because the swarm is the one part of this client that cannot be
// proven by a unit test: the fakes cover the decisions, but "does a blob
// actually arrive over the mesh" is a question about two machines and an
// underlay. This drives the real backend, the real enrolment loop and the real
// fetcher — everything cmd/madplayer does except paint.
//
//	go run ./cmd/meshcheck -base https://… -token … -hash … [-data DIR]
//
// The data directory defaults to a temporary one, so a run never touches the
// install at ~/.config/madplayer.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"daemonlord.ygg/madplayer/internal/backend"
	"daemonlord.ygg/madplayer/internal/blobcache"
	"daemonlord.ygg/madplayer/internal/library"
	"daemonlord.ygg/madplayer/internal/madshare"
	"daemonlord.ygg/madplayer/internal/mesh"
	"daemonlord.ygg/madplayer/internal/queue"
	"daemonlord.ygg/madplayer/internal/remote"
)

func main() {
	base := flag.String("base", "", "server base URL")
	token := flag.String("token", "", "API token for that server (or $MADPLAYER_TOKEN)")
	hash := flag.String("hash", "", "content hash(es) to fetch, comma-separated and fetched in order")
	data := flag.String("data", "", "data directory (default: a temporary one)")
	wait := flag.Duration("wait", 90*time.Second, "how long to give the fetch")
	budget := flag.Duration("budget", 0, "how long the swarm may take before the relay is used (0 = the player's default)")
	flag.Parse()

	if *token == "" {
		*token = os.Getenv("MADPLAYER_TOKEN")
	}
	if *base == "" || *token == "" || *hash == "" {
		log.Fatal("need -base, -hash and a token")
	}
	if err := run(*base, *token, *hash, *data, *wait, *budget); err != nil {
		log.Fatal(err)
	}
}

func run(base, token, hash, data string, wait, budget time.Duration) error {
	if data == "" {
		var err error
		if data, err = os.MkdirTemp("", "meshcheck"); err != nil {
			return err
		}
		defer os.RemoveAll(data)
	}
	fmt.Printf("data dir     %s\n", data)

	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()

	be, err := backend.Open(ctx, data, log.Default(), backend.Options{Mesh: true})
	if err != nil {
		return fmt.Errorf("open backend: %w", err)
	}
	defer be.Close()

	node, up := be.Mesh()
	if !up {
		return fmt.Errorf("the mesh is off: %s", be.MeshProblem())
	}
	fmt.Printf("node key     %s\n", node.Key())
	fmt.Printf("address      %s\n", node.Address())

	// Enrolment, exactly as the UI runs it: a vouch, a way onto the underlay, an
	// advertisement of what we hold.
	cl := madshare.New(base, token)
	enrol := mesh.New(node, log.Default())
	go enrol.Run(ctx)
	enrol.SetServers(ctx, []mesh.Server{{Base: base, Label: "home", Client: cl}})

	st, err := waitEnrolled(ctx, enrol, base)
	if err != nil {
		return err
	}
	fmt.Printf("enrolled     issuer=%s peers=%d advertised=%d\n", st.Key, st.Peers, st.Advertised)

	// Who the server says holds them. Printed because an empty list is the one
	// answer that makes everything below a relay download no matter what works.
	hashes := strings.Split(hash, ",")
	for _, h := range hashes {
		plan, err := cl.Holders(ctx, h)
		if err != nil {
			return fmt.Errorf("holders: %w", err)
		}
		fmt.Printf("holders      %s  %d holder(s), %d bytes\n", h[:16], len(plan.Keys()), plan.Size)
		for _, x := range plan.Holders {
			fmt.Printf("             %s  %s (last seen %s ago)\n", x.Key[:16], x.Name,
				time.Since(time.Unix(x.LastSeen, 0)).Round(time.Second))
		}
	}

	cache, err := blobcache.Open(filepath.Join(data, "remote"), 0)
	if err != nil {
		return err
	}
	f := remote.New(cache, log.Default())
	f.SetServers([]library.Server{{Base: base, Label: "home", Client: cl}})
	f.SetSwarm(be, enrol)
	// A diagnosis often wants to see how long the swarm ACTUALLY takes, which the
	// player's own budget exists to stop it from waiting for.
	f.SetSwarmBudget(budget)
	eff := budget
	if eff == 0 {
		eff = remote.DefaultSwarmBudget
	}
	fmt.Printf("swarm budget %s\n", eff)

	// Each hash in turn, in ONE process. The sequence is the measurement: if a
	// fresh node is slow because the underlay has not converged a route yet, the
	// first fetch pays for it and the rest do not — which is a completely
	// different problem from a throttle, and has a completely different fix.
	for i, h := range hashes {
		item := &queue.Item{
			URL:    base + "/files/" + h + "/track.flac",
			Hash:   h,
			Origin: "home",
		}
		fmt.Printf("\nfetch %d/%d  %s\n", i+1, len(hashes), h[:16])
		start := time.Now()
		path, err := f.Local(ctx, item)
		took := time.Since(start)
		if err != nil {
			fmt.Printf("             FAILED after %s: %v\n", took.Round(time.Millisecond), err)
			continue
		}
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		// Which path it took, read off the disk rather than from a log line: a
		// swarm fetch lands the blob in madshare's own cache on its way past, and a
		// relay download never touches it.
		route := "RELAY"
		if _, err := os.Stat(filepath.Join(data, "cache", "madnetwork", h)); err == nil {
			route = "SWARM"
		}
		fmt.Printf("             %s  %d bytes in %s  (seeding %d)\n",
			route, info.Size(), took.Round(time.Millisecond), len(node.Holdings()))
	}
	return nil
}

// waitEnrolled blocks until the first round with this server succeeded.
func waitEnrolled(ctx context.Context, e *mesh.Enrolment, base string) (mesh.Status, error) {
	for {
		for _, st := range e.Status() {
			if st.Base != base {
				continue
			}
			if !st.Enrolled.IsZero() {
				return st, nil
			}
			if st.Problem != "" {
				return st, errors.New("enrolment: " + st.Problem)
			}
		}
		select {
		case <-ctx.Done():
			return mesh.Status{}, errors.New("enrolment never completed")
		case <-time.After(200 * time.Millisecond):
		}
	}
}
