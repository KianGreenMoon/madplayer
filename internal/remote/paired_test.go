package remote

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"

	"daemonlord.ygg/madplayer/internal/blobcache"
	"daemonlord.ygg/madplayer/internal/library"
	"daemonlord.ygg/madplayer/internal/queue"
)

// The paired fetch path (node mode, docs/design.md §"Node mode"): a row browsed
// through this device's OWN node carries Base = library.NodeSourceBase, and its
// bytes are fetched on the node's own standing — the holder plan from the
// node's cached catalogs, NO vouch presented. The stakes are the two ways this
// could silently regress into the listener path: asking a server that does not
// exist for holders, or declining every fetch for want of a token no paired
// node needs.

type fakeDirectory struct {
	keys  []string
	asked int
}

func (d *fakeDirectory) CommunityHolders(context.Context, string) (int64, []string, error) {
	d.asked++
	return 6, d.keys, nil
}

// pairedSwarm records what it was asked to fetch and answers fixed bytes.
type pairedSwarm struct {
	holders []string
	body    []byte
}

func (s *pairedSwarm) StreamBlob(_ context.Context, _ string, _ int64, holders []string, _ time.Duration) (io.ReadCloser, error) {
	s.holders = holders
	return io.NopCloser(bytes.NewReader(s.body)), nil
}

// refusingVouch fails every Present — the state a paired-but-signed-out device
// is in, and exactly what a paired fetch must not consult.
type refusingVouch struct{ asked bool }

func (v *refusingVouch) Present(string) bool { v.asked = true; return false }

func pairedItem() *queue.Item {
	return &queue.Item{
		Network: true,
		Base:    library.NodeSourceBase,
		Hash:    "abc123",
		Codec:   "mp3",
		Title:   "Endure Emptiness",
	}
}

func TestPairedRowFetchesOnTheNodesOwnStanding(t *testing.T) {
	cache, err := blobcache.Open(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	f := New(cache, quiet())
	swarm := &pairedSwarm{body: []byte("audio-bytes")}
	vouch := &refusingVouch{}
	dir := &fakeDirectory{keys: []string{"k1", "k2"}}
	f.SetSwarm(swarm, vouch)
	f.SetDirectory(dir)

	rc, _, err := f.Stream(context.Background(), pairedItem())
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	got, err := io.ReadAll(rc)
	rc.Close()
	if err != nil || string(got) != "audio-bytes" {
		t.Fatalf("read = %q, %v; want the swarm's bytes", got, err)
	}
	if dir.asked == 0 {
		t.Error("the own node was never asked for the holder plan")
	}
	if len(swarm.holders) != 2 {
		t.Errorf("swarm asked with %v; want the directory's two keys", swarm.holders)
	}
	if vouch.asked {
		t.Error("a paired fetch presented (or consulted) a vouch — friendship is its standing")
	}
}

// With node mode off there is no directory, and the item must DECLINE with a
// sentence rather than error or reach for a server — the same politeness every
// other swarm decline has.
func TestPairedRowDeclinesWithoutTheNode(t *testing.T) {
	cache, err := blobcache.Open(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	f := New(cache, quiet())
	f.SetSwarm(&pairedSwarm{body: []byte("x")}, nil)

	// The fill runs behind the reader, so the decline surfaces on the READ —
	// same shape as every other streaming failure.
	rc, _, err := f.Stream(context.Background(), pairedItem())
	if err == nil {
		_, err = io.ReadAll(rc)
		rc.Close()
	}
	if err == nil {
		t.Fatal("a paired row with no directory played something")
	}
	if want := "node mode is off"; !bytes.Contains([]byte(err.Error()), []byte(want)) {
		t.Errorf("err = %v; want it to say %q", err, want)
	}
}
