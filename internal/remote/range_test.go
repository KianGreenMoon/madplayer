package remote

// The byte-addressed surface the player's seek path stands on: TrackSize asks
// once and remembers, OpenRange is a Range request that starts where asked.

import (
	"context"
	"io"
	"testing"
)

func TestTrackSizeIsAskedOnceThenRemembered(t *testing.T) {
	ms := newMeshServer(t, "twenty-five bytes of song.")
	f := meshFetcher(t, ms, nil, nil)
	item := ms.track(hashP)

	n, err := f.TrackSize(context.Background(), item)
	if err != nil {
		t.Fatal(err)
	}
	if want := int64(len("twenty-five bytes of song.")); n != want {
		t.Fatalf("size = %d, want %d", n, want)
	}
	if _, err := f.TrackSize(context.Background(), item); err != nil {
		t.Fatal(err)
	}
	if got := ms.heads.Load(); got != 1 {
		t.Errorf("the server was HEADed %d time(s), want once — the second ask is remembered", got)
	}
}

func TestOpenRangeStartsWhereAsked(t *testing.T) {
	const body = "the whole of the track's audio"
	ms := newMeshServer(t, body)
	f := meshFetcher(t, ms, nil, nil)

	rc, err := f.OpenRange(context.Background(), ms.track(hashP), 10)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != body[10:] {
		t.Fatalf("read %q, want the bytes from offset 10", got)
	}
	if rng := ms.lastRange.Load(); rng == nil || *rng != "bytes=10-" {
		t.Errorf("the server saw Range %v, want bytes=10-", rng)
	}
}
