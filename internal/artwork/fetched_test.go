package artwork

import (
	"bytes"
	"errors"
	"image"
	"image/png"
	"sync/atomic"
	"testing"
	"time"
)

// GetFetched: network art through the same cache as file art — fetched once,
// shrunk once, and a failure remembered as the same "none" a coverless folder
// gets, so a 404ing server is asked once and not sixty times a second.

func fetchedSettled(t *testing.T, c *Cache, key string, fetch func() ([]byte, error)) image.Image {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		img, settled := c.GetFetched(key, fetch)
		if settled {
			return img
		}
		if time.Now().After(deadline) {
			t.Fatalf("cover %q never settled", key)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestGetFetchedFetchesOnceAndShrinks(t *testing.T) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, MaxDimension*2, MaxDimension*2))); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	fetch := func() ([]byte, error) {
		calls.Add(1)
		return buf.Bytes(), nil
	}

	c := New()
	img := fetchedSettled(t, c, "net!k1", fetch)
	if img == nil {
		t.Fatal("a decodable cover settled to none")
	}
	if b := img.Bounds(); b.Dx() > MaxDimension || b.Dy() > MaxDimension {
		t.Errorf("cover kept at %v, want shrunk to %d", b, MaxDimension)
	}
	// Asking again is a cache hit, not a second fetch.
	if img2 := fetchedSettled(t, c, "net!k1", fetch); img2 == nil {
		t.Fatal("the settled cover vanished")
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("fetch ran %d times, want once", n)
	}
}

func TestGetFetchedRemembersFailure(t *testing.T) {
	var calls atomic.Int32
	fetch := func() ([]byte, error) {
		calls.Add(1)
		return nil, errors.New("the server has no such cover")
	}
	c := New()
	if img := fetchedSettled(t, c, "net!gone", fetch); img != nil {
		t.Fatal("a failed fetch settled to an image")
	}
	fetchedSettled(t, c, "net!gone", fetch) // and the answer is remembered
	if n := calls.Load(); n != 1 {
		t.Errorf("fetch ran %d times, want once — 'none' is an answer too", n)
	}
}

func TestGetFetchedUndecodableIsNone(t *testing.T) {
	c := New()
	img := fetchedSettled(t, c, "net!junk", func() ([]byte, error) {
		return []byte("this is not an image"), nil
	})
	if img != nil {
		t.Fatal("undecodable bytes settled to an image")
	}
}
