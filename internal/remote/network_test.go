package remote

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"daemonlord.ygg/madplayer/internal/queue"
)

// Playing music the madnetwork holds, from a player.
//
// The rule these pin is the owner's, decided 2026-08-15 when the browse half was
// built: the home server is a DIRECTORY for network content and never a source
// of bytes. It says who holds a hash; the holders send it. There is an endpoint
// that would do the whole job — /api/madnetwork/stream/{hash} — and it is a
// cache-through relay, so using it would fill that machine's disk with the
// community's catalogue as a side effect of somebody browsing it here.
//
// Which makes the swarm not the preferred path for these tracks but the only
// one, and that is a thing worth a test rather than a comment: the fallback it
// replaced is still there, three lines below, for every other kind of track.

// networkItem is a track the catalogue named and no server offers a URL for.
func networkItem(ms *meshServer, hash string) *queue.Item {
	return &queue.Item{
		Hash:    hash,
		Network: true,
		Base:    ms.URL,
		Codec:   "flac",
		Origin:  "madnetwork",
		Title:   "Endure Emptiness",
	}
}

func TestANetworkTrackPlaysFromTheHoldersAndNotFromTheServer(t *testing.T) {
	ms := newMeshServer(t, "FLAC bytes", "holder-key")
	sw := &fakeSwarm{body: "FLAC bytes"}
	f := meshFetcher(t, ms, sw, &fakeVouch{ok: true})

	path, err := f.Local(context.Background(), networkItem(ms, hashA))
	if err != nil {
		t.Fatal(err)
	}
	if got := read(t, path); got != "FLAC bytes" {
		t.Errorf("played %q", got)
	}
	if sw.calls != 1 {
		t.Errorf("swarm fetches = %d, want 1 — the mesh is the whole path here", sw.calls)
	}
	if n := ms.relay.Load(); n != 0 {
		t.Errorf("the home server was asked for %d download(s) — it is a directory, not a source", n)
	}
	if n := ms.holders.Load(); n != 1 {
		t.Errorf("holder lookups = %d, want 1", n)
	}
}

// The one that matters most: when the mesh cannot deliver, the answer is a
// sentence, NOT a download through the home server.
//
// Every other kind of track falls back to the relay here, and that fallback is
// right — it is level 1, it works with no mesh at all. Network content is the
// exception because the fallback would make somebody else's server fetch and
// keep audio that nobody asked it to hold.
func TestANetworkTrackNeverFallsBackToTheRelay(t *testing.T) {
	for _, tc := range []struct {
		name string
		sw   *fakeSwarm
		keys []string
		want string
	}{
		{name: "the swarm fails", sw: &fakeSwarm{err: errors.New("no route")}, keys: []string{"holder-key"},
			want: "could not send"},
		{name: "nobody holds it", sw: &fakeSwarm{body: "FLAC bytes"}, keys: nil,
			want: "nobody reachable has this track"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ms := newMeshServer(t, "FLAC bytes", tc.keys...)
			f := meshFetcher(t, ms, tc.sw, &fakeVouch{ok: true})

			_, err := f.Local(context.Background(), networkItem(ms, hashA))
			if err == nil {
				t.Fatal("a track nobody could send played anyway — from where?")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to say %q", err, tc.want)
			}
			if n := ms.relay.Load(); n != 0 {
				t.Errorf("the home server was asked for %d download(s) — that is the cache-through this avoids", n)
			}
		})
	}
}

// A device with no vouch yet declines the swarm silently, which for an ordinary
// track means the relay. A network track has no relay, so the decline has to
// surface as an error rather than as silence — the alternative is a click that
// does nothing.
func TestANetworkTrackWithNoVouchSaysSoInsteadOfHanging(t *testing.T) {
	ms := newMeshServer(t, "FLAC bytes", "holder-key")
	sw := &fakeSwarm{body: "FLAC bytes"}
	f := meshFetcher(t, ms, sw, &fakeVouch{ok: false})

	_, err := f.Local(context.Background(), networkItem(ms, hashA))
	if err == nil {
		t.Fatal("a fetch with no vouch to present reported success")
	}
	// The reason has to be the RIGHT one. Every silent decline used to end at
	// the relay, so one message could stand for all of them; with no relay
	// behind it, "nobody has this track" would send somebody looking for the
	// track when the missing thing is this device's standing on the mesh.
	if !strings.Contains(err.Error(), "vouch") {
		t.Errorf("error = %q, want it to name the missing vouch", err)
	}
	if sw.calls != 0 {
		t.Error("the swarm was used without a vouch installed")
	}
	if n := ms.relay.Load(); n != 0 {
		t.Errorf("the home server was asked for %d download(s)", n)
	}
}

// The cache file needs an extension or nothing can decode it, and a madnetwork
// copy has no filename anywhere — only the catalogue's codec.
func TestANetworkTrackIsCachedUnderItsCodecsExtension(t *testing.T) {
	ms := newMeshServer(t, "MP3 bytes", "holder-key")
	f := meshFetcher(t, ms, &fakeSwarm{body: "MP3 bytes"}, &fakeVouch{ok: true})

	item := networkItem(ms, hashA)
	item.Codec = "mp3"
	path, err := f.Local(context.Background(), item)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(path, ".mp3") {
		t.Errorf("cached as %q — the decoders pick by extension, so audio without one is unplayable", path)
	}
}

// The same audio reached two ways is ONE file on disk: the cache is keyed by
// content hash, which is the whole reason a hash travels with a queue item.
func TestTheSameAudioFromTheServerAndTheNetworkIsCachedOnce(t *testing.T) {
	ms := newMeshServer(t, "FLAC bytes", "holder-key")
	sw := &fakeSwarm{body: "FLAC bytes"}
	f := meshFetcher(t, ms, sw, &fakeVouch{ok: true})

	byNetwork, err := f.Local(context.Background(), networkItem(ms, hashA))
	if err != nil {
		t.Fatal(err)
	}
	// The server's own copy of the same blob, addressed by URL.
	byServer, err := f.Local(context.Background(), ms.track(hashA))
	if err != nil {
		t.Fatal(err)
	}
	if byNetwork != byServer {
		t.Errorf("two files for one blob:\n  %s\n  %s", byNetwork, byServer)
	}
	if n := ms.relay.Load(); n != 0 {
		t.Errorf("the second fetch downloaded again (%d relay hits) instead of finding the file", n)
	}
}

// A network item that cannot name its server is refused before anything is
// asked, because "who holds this?" has nobody to ask.
func TestANetworkItemWithNoServerIsRefusedNotGuessed(t *testing.T) {
	ms := newMeshServer(t, "FLAC bytes", "holder-key")
	f := meshFetcher(t, ms, &fakeSwarm{body: "FLAC bytes"}, &fakeVouch{ok: true})

	item := networkItem(ms, hashA)
	item.Base = ""
	if _, err := f.Local(context.Background(), item); err == nil {
		t.Fatal("an item with no server to ask was fetched from somewhere")
	}
}

// Streaming takes the same path as Local — one fill, so "play this" and "keep
// this" cannot drift about where the bytes come from.
func TestStreamingANetworkTrackAlsoAvoidsTheServer(t *testing.T) {
	ms := newMeshServer(t, "FLAC bytes", "holder-key")
	sw := &fakeSwarm{body: "FLAC bytes"}
	f := meshFetcher(t, ms, sw, &fakeVouch{ok: true})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rc, ext, err := f.Stream(ctx, networkItem(ms, hashA))
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	if ext != ".flac" {
		t.Errorf("ext = %q, want the codec's", ext)
	}
	if n := ms.relay.Load(); n != 0 {
		t.Errorf("streaming went through the server (%d hits)", n)
	}
}
