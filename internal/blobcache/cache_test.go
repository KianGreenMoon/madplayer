package blobcache

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func write(b []byte) Fetch {
	return func(ctx context.Context, w io.Writer) error {
		_, err := w.Write(b)
		return err
	}
}

func TestGetFetchesOnceThenHits(t *testing.T) {
	c, err := Open(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	var fetches atomic.Int32
	fetch := func(ctx context.Context, w io.Writer) error {
		fetches.Add(1)
		_, err := w.Write([]byte("audio"))
		return err
	}

	path, err := c.Get(context.Background(), "k1", ".mp3", fetch)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got, _ := os.ReadFile(path); string(got) != "audio" {
		t.Errorf("contents = %q", got)
	}
	if _, err := c.Get(context.Background(), "k1", ".mp3", fetch); err != nil {
		t.Fatalf("second Get: %v", err)
	}
	if n := fetches.Load(); n != 1 {
		t.Errorf("fetched %d times, want 1", n)
	}
}

// The prefetcher and a click on the very same track must not both download it.
func TestConcurrentGetsShareOneFetch(t *testing.T) {
	c, err := Open(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	var fetches atomic.Int32
	release := make(chan struct{})
	fetch := func(ctx context.Context, w io.Writer) error {
		fetches.Add(1)
		<-release
		_, err := w.Write([]byte("audio"))
		return err
	}

	var wg sync.WaitGroup
	paths := make([]string, 4)
	errs := make([]error, 4)
	for i := range paths {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			paths[i], errs[i] = c.Get(context.Background(), "k1", ".mp3", fetch)
		}(i)
	}
	// Let every caller arrive before the fetch completes.
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()

	if n := fetches.Load(); n != 1 {
		t.Errorf("fetched %d times, want 1", n)
	}
	for i := range paths {
		if errs[i] != nil {
			t.Fatalf("caller %d: %v", i, errs[i])
		}
		if paths[i] != paths[0] {
			t.Errorf("caller %d got %q, want %q", i, paths[i], paths[0])
		}
	}
}

// Skipping past a track should abandon its download, not pay for it silently in
// the background.
func TestFetchStopsWhenEveryWaiterLeaves(t *testing.T) {
	c, err := Open(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	cancelled := make(chan struct{})
	fetch := func(ctx context.Context, w io.Writer) error {
		<-ctx.Done()
		close(cancelled)
		return ctx.Err()
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := c.Get(ctx, "k1", ".mp3", fetch); !errors.Is(err, context.Canceled) {
			t.Errorf("Get err = %v, want context.Canceled", err)
		}
	}()
	time.Sleep(30 * time.Millisecond)
	cancel()
	<-done

	select {
	case <-cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("the fetch kept running after its last waiter left")
	}
}

func TestAFailedFetchLeavesNothingBehind(t *testing.T) {
	dir := t.TempDir()
	c, err := Open(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	boom := errors.New("connection reset")
	_, err = c.Get(context.Background(), "k1", ".mp3", func(ctx context.Context, w io.Writer) error {
		_, _ = w.Write([]byte("half a fi"))
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the fetch error", err)
	}

	// A half-written file presented as a track decodes into noise and looks like
	// a corrupt original.
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("cache holds %v after a failed fetch, want nothing", names(entries))
	}
}

func TestEvictionDropsTheLeastRecentlyUsed(t *testing.T) {
	dir := t.TempDir()
	// Room for two 100-byte blobs.
	c, err := Open(dir, 250)
	if err != nil {
		t.Fatal(err)
	}
	blob := make([]byte, 100)

	for _, k := range []string{"old", "mid"} {
		if _, err := c.Get(context.Background(), k, ".mp3", write(blob)); err != nil {
			t.Fatal(err)
		}
	}
	// Age them apart: "old" is the oldest use, "mid" newer.
	age(t, filepath.Join(dir, "old.mp3"), -2*time.Hour)
	age(t, filepath.Join(dir, "mid.mp3"), -1*time.Hour)

	if _, err := c.Get(context.Background(), "new", ".mp3", write(blob)); err != nil {
		t.Fatal(err)
	}

	if exists(filepath.Join(dir, "old.mp3")) {
		t.Error("the least recently used blob survived eviction")
	}
	for _, keep := range []string{"mid.mp3", "new.mp3"} {
		if !exists(filepath.Join(dir, keep)) {
			t.Errorf("%s was evicted; only the coldest should be", keep)
		}
	}
}

// A hit is a use: playing an old track again must save it from the next eviction.
func TestAHitCountsAsUse(t *testing.T) {
	dir := t.TempDir()
	c, err := Open(dir, 250)
	if err != nil {
		t.Fatal(err)
	}
	blob := make([]byte, 100)
	for _, k := range []string{"a", "b"} {
		if _, err := c.Get(context.Background(), k, ".mp3", write(blob)); err != nil {
			t.Fatal(err)
		}
	}
	age(t, filepath.Join(dir, "a.mp3"), -2*time.Hour)
	age(t, filepath.Join(dir, "b.mp3"), -1*time.Hour)

	// Play "a" again — now "b" is the coldest.
	if _, ok := c.Lookup("a", ".mp3"); !ok {
		t.Fatal("a should be cached")
	}
	if _, err := c.Get(context.Background(), "c", ".mp3", write(blob)); err != nil {
		t.Fatal(err)
	}

	if !exists(filepath.Join(dir, "a.mp3")) {
		t.Error("a was evicted even though it had just been played")
	}
	if exists(filepath.Join(dir, "b.mp3")) {
		t.Error("b should have been the eviction victim")
	}
}

// Making room by deleting the very thing that was asked for is a cache that
// never hits.
func TestEvictionNeverDropsTheBlobJustFetched(t *testing.T) {
	dir := t.TempDir()
	c, err := Open(dir, 10) // far below one blob
	if err != nil {
		t.Fatal(err)
	}
	path, err := c.Get(context.Background(), "big", ".mp3", write(make([]byte, 100)))
	if err != nil {
		t.Fatal(err)
	}
	if !exists(path) {
		t.Fatal("the blob just fetched was evicted immediately")
	}
}

func TestSetLimitReclaimsSpaceNow(t *testing.T) {
	dir := t.TempDir()
	c, err := Open(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	blob := make([]byte, 100)
	for _, k := range []string{"a", "b", "c"} {
		if _, err := c.Get(context.Background(), k, ".mp3", write(blob)); err != nil {
			t.Fatal(err)
		}
		age(t, filepath.Join(dir, k+".mp3"), -time.Duration(3-len(k))*time.Hour)
	}
	age(t, filepath.Join(dir, "a.mp3"), -3*time.Hour)
	age(t, filepath.Join(dir, "b.mp3"), -2*time.Hour)
	age(t, filepath.Join(dir, "c.mp3"), -1*time.Hour)

	c.SetLimit(150)

	size, err := c.Size()
	if err != nil {
		t.Fatal(err)
	}
	if size > 150 {
		t.Errorf("size = %d after lowering the ceiling to 150", size)
	}
	if !exists(filepath.Join(dir, "c.mp3")) {
		t.Error("the newest blob should have survived")
	}
}

func TestOpenReapsPartialsFromAPreviousRun(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "abandoned.mp3.part"), []byte("half"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "whole.mp3"), []byte("all"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir, 0); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(dir, "abandoned.mp3.part")) {
		t.Error("a partial from a previous run survived startup")
	}
	if !exists(filepath.Join(dir, "whole.mp3")) {
		t.Error("a complete blob was reaped")
	}
}

// A content hash is used as-is, so the same audio fetched from two servers is
// stored once.
func TestKeyPassesAContentHashThrough(t *testing.T) {
	hash := "3f786850e387550fdab836ed7e6dc881de23001b3f786850e387550fdab836ed"
	if got := Key(hash); got != hash {
		t.Errorf("Key(hash) = %q, want it used as-is", got)
	}
	url := "http://host:3000/files/abc/song.mp3"
	if got := Key(url); got == url || len(got) != 64 {
		t.Errorf("Key(url) = %q, want a hex digest", got)
	}
	if Key(url) != Key(url) {
		t.Error("Key is not stable")
	}
}

func age(t *testing.T, path string, d time.Duration) {
	t.Helper()
	when := time.Now().Add(d)
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatal(err)
	}
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func names(entries []os.DirEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}
