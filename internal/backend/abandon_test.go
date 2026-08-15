package backend

// Transfers run on the NODE's lifetime, not the caller's context — that is
// madshare's deliberate cache-through shape — so on a player, every path that
// stops wanting a transfer must say so with Abandon. Before it existed, a
// swarm fetch that missed the first-byte budget ran to completion in the
// background, in parallel with the relay downloading the very same bytes
// (.issues/open-issues.md row 1, 2026-08-15). These tests pin the three
// abandonment sites in this package.

import (
	"context"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"daemonlord.ygg/madshare/federation"
)

// stubTransfer is a swarm transfer the test controls byte by byte.
type stubTransfer struct {
	path      string
	progress  atomic.Int64
	done      chan struct{}
	err       error
	abandoned atomic.Int32
}

func newStubTransfer(t *testing.T, content string) *stubTransfer {
	t.Helper()
	path := filepath.Join(t.TempDir(), "blob")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return &stubTransfer{path: path, done: make(chan struct{})}
}

func (s *stubTransfer) Hash() string            { return "aa" }
func (s *stubTransfer) Size() int64             { return 0 }
func (s *stubTransfer) Filename() string        { return "" }
func (s *stubTransfer) Progress() int64         { return s.progress.Load() }
func (s *stubTransfer) Done() <-chan struct{}   { return s.done }
func (s *stubTransfer) Err() error              { return s.err }
func (s *stubTransfer) Open() (*os.File, error) { return os.Open(s.path) }
func (s *stubTransfer) Abandon()                { s.abandoned.Add(1) }
func (s *stubTransfer) Stats() federation.TransferStats {
	return federation.TransferStats{Hash: "aa"}
}
func (s *stubTransfer) Available(offset int64) int64 {
	if p := s.progress.Load(); p > offset {
		return p - offset
	}
	return 0
}
func (s *stubTransfer) WaitFor(ctx context.Context, offset int64) error {
	for {
		if s.progress.Load() > offset {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.done:
			return io.EOF
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// stubNet hands out one prepared transfer.
type stubNet struct{ t *stubTransfer }

func (n stubNet) Key() string                                           { return "" }
func (n stubNet) Address() string                                       { return "" }
func (n stubNet) SetToken(string)                                       {}
func (n stubNet) AddHome(context.Context, string, string, string) error { return nil }
func (n stubNet) RemoveHome(context.Context, string) error              { return nil }
func (n stubNet) Homes(context.Context) ([]federation.ExternalNode, error) {
	return nil, nil
}
func (n stubNet) Holdings() []string                   { return nil }
func (n stubNet) EvictCached(string) error             { return nil }
func (n stubNet) AddPeer(string) error                 { return nil }
func (n stubNet) PublishNothing(context.Context) error { return nil }
func (n stubNet) Fetch(context.Context, string, int64, []string) (federation.Transfer, error) {
	return n.t, nil
}

func quietBackend(tr *stubTransfer) *Backend {
	return &Backend{net: stubNet{t: tr}, log: log.New(io.Discard, "", 0)}
}

// A swarm that misses the first-byte budget is declined — and the transfer is
// ABANDONED, not left downloading the whole track beside the relay fallback.
func TestStreamBlobAbandonsTheTransferOnADeclinedFirstByte(t *testing.T) {
	tr := newStubTransfer(t, "")
	be := quietBackend(tr)

	_, err := be.StreamBlob(context.Background(), "aa", 0, []string{"bb"}, 50*time.Millisecond)
	if err == nil {
		t.Fatal("a first byte that never came was not an error")
	}
	if tr.abandoned.Load() == 0 {
		t.Fatal("the declined transfer was left running")
	}
}

// A reader closed mid-transfer (the track was skipped) abandons the run: it is
// the transfer's only consumer, and without this the swarm kept downloading
// the rest for nobody.
func TestClosingTheSwarmReaderMidTransferAbandonsIt(t *testing.T) {
	tr := newStubTransfer(t, "some bytes")
	tr.progress.Store(4) // mid-transfer: first bytes readable, more promised
	be := quietBackend(tr)

	rc, err := be.StreamBlob(context.Background(), "aa", 0, []string{"bb"}, time.Second)
	if err != nil {
		t.Fatalf("StreamBlob: %v", err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(rc, buf); err != nil {
		t.Fatalf("first read: %v", err)
	}
	rc.Close()
	if tr.abandoned.Load() == 0 {
		t.Fatal("closing the last reader did not abandon the transfer")
	}
}

// The blocking FetchBlob's caller walking away abandons the transfer too.
func TestFetchBlobAbandonsOnACancelledWait(t *testing.T) {
	tr := newStubTransfer(t, "")
	be := quietBackend(tr)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := be.FetchBlob(ctx, "aa", 0, []string{"bb"}); err == nil {
		t.Fatal("an expired wait was not an error")
	}
	if tr.abandoned.Load() == 0 {
		t.Fatal("the walked-away-from transfer was left running")
	}
}
