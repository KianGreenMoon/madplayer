package remote

// Resuming a madnetwork track that died mid-stream (owner's call, 2026-08-23 —
// option C of the skip diagnosis; the lab that reproduced the skips is
// internal/backend/labnet_test.go).
//
// A network row has no relay behind it, so before this a mid-stream death WAS
// the track: the swarm's own failover rides out a few seconds of a holder's
// silence, and anything longer — a wifi handover, a router reconnect, a holder
// restarting — skipped a song that was audibly playing. Now the fill retries a
// bounded number of times, each with a fresh holder plan and a freshly
// presented vouch, splicing at the byte count already written. What must stay
// true around that: a fetch that never produced a byte is NOT retried, and a
// resume that cannot finish must fail the track rather than leave a short file
// pretending to be a finished one.

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

// dying returns a stream factory that delivers its prefix and then fails —
// the shape of a holder vanishing mid-transfer.
func dying(prefix string, err error) func() (io.Reader, error) {
	return func() (io.Reader, error) {
		return io.MultiReader(strings.NewReader(prefix), failingReader{err}), nil
	}
}

// whole returns a stream factory that delivers the full blob.
func whole(body string) func() (io.Reader, error) {
	return func() (io.Reader, error) { return strings.NewReader(body), nil }
}

type failingReader struct{ err error }

func (r failingReader) Read([]byte) (int, error) { return 0, r.err }

// immediate makes the resumes free to wait for, so a test does not sit out
// real seconds. The slice length still decides how many resumes there are.
func immediate(f *Fetcher, n int) {
	f.resumeWaits = make([]time.Duration, n)
}

func TestANetworkTrackResumesAfterAMidStreamDeath(t *testing.T) {
	const full = "FLAC bytes, all sixteen"
	ms := newMeshServer(t, full, "holder-key")
	sw := &fakeSwarm{script: []func() (io.Reader, error){
		dying(full[:7], errors.New("holder went away")),
		whole(full),
	}}
	f := meshFetcher(t, ms, sw, &fakeVouch{ok: true})
	immediate(f, 2)

	path, err := f.Local(context.Background(), networkItem(ms, hashA))
	if err != nil {
		t.Fatalf("a track whose stream died once did not recover: %v", err)
	}
	if got := read(t, path); got != full {
		t.Errorf("played %q, want the spliced whole — a wrong splice here is audible noise", got)
	}
	if sw.calls != 2 {
		t.Errorf("swarm fetches = %d, want 2 — one death, one resume", sw.calls)
	}
	// Each resume asks who holds the track NOW: the plan that died may be the
	// thing that was wrong with it.
	if n := ms.holders.Load(); n != 2 {
		t.Errorf("holder lookups = %d, want a fresh plan per attempt (2)", n)
	}
}

func TestANetworkTrackGivesUpAfterItsResumes(t *testing.T) {
	const full = "FLAC bytes, all sixteen"
	ms := newMeshServer(t, full, "holder-key")
	sw := &fakeSwarm{script: []func() (io.Reader, error){
		dying(full[:5], errors.New("still down")),
	}}
	f := meshFetcher(t, ms, sw, &fakeVouch{ok: true})
	immediate(f, 2)

	item := networkItem(ms, hashA)
	_, err := f.Local(context.Background(), item)
	if err == nil {
		t.Fatal("a stream that died on every attempt reported success")
	}
	if !strings.Contains(err.Error(), "could not send") {
		t.Errorf("error = %q, want the madnetwork failure sentence", err)
	}
	if sw.calls != 3 {
		t.Errorf("swarm fetches = %d, want the first plus two resumes (3)", sw.calls)
	}
	if f.Cached(item) {
		t.Error("the failed fetch left a cached copy — every replay would be a short file")
	}
}

// A fetch that never produced a byte is not resumed: the swarm already spent
// its own attempts and the caller's budget, and what to do next is the
// person's decision, not a background loop's.
func TestAFetchThatNeverStartedIsNotRetried(t *testing.T) {
	ms := newMeshServer(t, "FLAC bytes", "holder-key")
	sw := &fakeSwarm{err: errors.New("no route")}
	f := meshFetcher(t, ms, sw, &fakeVouch{ok: true})
	immediate(f, 2)

	if _, err := f.Local(context.Background(), networkItem(ms, hashA)); err == nil {
		t.Fatal("a swarm with no route reported success")
	}
	if sw.calls != 1 {
		t.Errorf("swarm fetches = %d, want 1 — nothing was written, nothing to resume", sw.calls)
	}
}

// fadingVouch presents once and then refuses — the standing evaporating
// between the first attempt and its resume, as it does when a token cannot be
// renewed.
type fadingVouch struct{ calls int }

func (v *fadingVouch) Present(string) bool {
	v.calls++
	return v.calls == 1
}

func TestAResumeThatIsDeclinedFailsTheTrackInsteadOfShorteningIt(t *testing.T) {
	const full = "FLAC bytes, all sixteen"
	ms := newMeshServer(t, full, "holder-key")
	sw := &fakeSwarm{script: []func() (io.Reader, error){
		dying(full[:9], errors.New("holder went away")),
	}}
	f := meshFetcher(t, ms, sw, &fadingVouch{})
	immediate(f, 1)

	item := networkItem(ms, hashA)
	_, err := f.Local(context.Background(), item)
	if err == nil {
		t.Fatal("a resume the vouch declined reported success over a short file")
	}
	if !strings.Contains(err.Error(), "stopped sending") || !strings.Contains(err.Error(), "vouch") {
		t.Errorf("error = %q, want the stopped-mid-track sentence naming the missing vouch", err)
	}
	if f.Cached(item) {
		t.Error("a short file was kept as a finished track")
	}
}
