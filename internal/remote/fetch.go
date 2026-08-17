// Package remote downloads tracks that live on somebody else's madshare and
// hands the player a local file.
//
// It is the join between three things that deliberately know nothing about each
// other: the queue (which carries a URL), the HTTP client (which knows the
// credential for one server) and the cache (which knows what is already on
// disk). The player asks for a path and gets one; that a download happened is
// not its business.
//
// There are two ways to get the bytes and this package owns the choice between
// them: the SWARM, which asks whoever holds the blob, and the RELAY, which asks
// the one server that named it. The swarm is preferred and the relay is not a
// degraded mode — it is level 1, it works with no mesh at all, and every reason
// the swarm has to decline ends here (docs/design.md §"Level 2b,
// concretely").
package remote

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"daemonlord.ygg/madplayer/internal/blobcache"
	"daemonlord.ygg/madplayer/internal/library"
	"daemonlord.ygg/madplayer/internal/madshare"
	"daemonlord.ygg/madplayer/internal/queue"
)

// Swarm is the mesh side of a fetch: bytes from whoever holds them, rather than
// from the one server that named them.
//
// Declared here rather than imported so that internal/backend stays the only
// package touching madshare — and narrowed all the way to "give me a reader",
// because that is genuinely all this package wants. A transfer's progress, its
// chunk accounting and its partial reads are a streaming caller's business, and
// a client whose decoders demand the whole file is not one.
type Swarm interface {
	// StreamBlob hands over a reader as soon as the first byte lands, and
	// firstByte is how long to wait for that. The transfer itself runs under
	// ctx and is not otherwise bounded — see fromSwarm for why the deadline
	// moved off the whole download and onto its start.
	StreamBlob(ctx context.Context, hash string, size int64, holders []string, firstByte time.Duration) (io.ReadCloser, error)
}

// Vouch installs the capability token a mesh fetch presents, and reports whether
// there was one to install.
//
// It is internal/mesh.Enrolment narrowed to the single thing a fetch needs from
// it. The narrowing says something true about the split: enrolment is about
// standing and this package is about content, and the one place they meet is the
// moment before bytes move.
type Vouch interface {
	Present(base string) bool
}

// Fetcher satisfies player.Fetcher.
type Fetcher struct {
	cache *blobcache.Cache
	log   *log.Logger

	mu          sync.RWMutex
	servers     []library.Server
	swarm       Swarm
	vouch       Vouch
	swarmBudget time.Duration

	// slot serializes mesh fetches — a one-place semaphore rather than a mutex,
	// because WAITING for it must be boundable. The mesh carries ONE vouch,
	// installed process-wide, so a second fetch starting underneath the first
	// would present its own token and break it — see fromSwarm. Since fetches
	// stream, a transfer can hold the slot for minutes; a mutex here once let a
	// clicked track wait out a prefetch's whole transfer before making a sound
	// (.issues/open-issues.md, reproduced 2026-08-15). Now a contender that
	// cannot take the slot within the budget declines to the relay instead.
	slot chan struct{}

	// prefetch is the speculative download of the NEXT track, cancelled as soon
	// as a different one is wanted. Without the cancel, changing your mind about
	// what to play next would leave the old guess downloading.
	pmu          sync.Mutex
	prefetchKey  string
	prefetchStop context.CancelFunc

	// sizes remembers each track's byte size (TrackSize), so a second seek on
	// the same track costs no HEAD request.
	szmu  sync.Mutex
	sizes map[string]int64
}

// New returns a fetcher over a cache. It fetches over the relay until SetSwarm
// says this device is on the mesh.
func New(cache *blobcache.Cache, lg *log.Logger) *Fetcher {
	if lg == nil {
		lg = log.Default()
	}
	return &Fetcher{cache: cache, log: lg, slot: make(chan struct{}, 1), sizes: map[string]int64{}}
}

// SetSwarm installs the mesh path, or clears it with two nils. Until it is
// called — and on a device that is not a node at all — every download is a relay
// download, which is the whole of level 1 and a working program.
func (f *Fetcher) SetSwarm(swarm Swarm, vouch Vouch) {
	f.mu.Lock()
	f.swarm, f.vouch = swarm, vouch
	f.mu.Unlock()
}

// DefaultSwarmBudget is how long a mesh fetch may take before the relay is used
// instead.
//
// It is a number about PEOPLE, not about networks: it is roughly how long
// somebody will wait after clicking a track before deciding the program is
// broken. The mesh is worth trying because it can be faster and because it is
// what makes this device useful to the household — it is not worth a silent
// stall, and on a badly-connected mesh the difference is minutes.
const DefaultSwarmBudget = 20 * time.Second

// SetSwarmBudget overrides that. Zero restores the default.
func (f *Fetcher) SetSwarmBudget(d time.Duration) {
	f.mu.Lock()
	f.swarmBudget = d
	f.mu.Unlock()
}

func (f *Fetcher) budget() time.Duration {
	f.mu.RLock()
	defer f.mu.RUnlock()
	if f.swarmBudget > 0 {
		return f.swarmBudget
	}
	return DefaultSwarmBudget
}

// SetServers replaces the signed-in servers. A queue item whose server has gone
// then fails with a sentence saying so, rather than a 401.
func (f *Fetcher) SetServers(servers []library.Server) {
	f.mu.Lock()
	f.servers = append([]library.Server(nil), servers...)
	f.mu.Unlock()
}

// Local returns a path to the item's audio, downloading it if this machine does
// not have it yet.
func (f *Fetcher) Local(ctx context.Context, item *queue.Item) (string, error) {
	if item.Path != "" {
		return item.Path, nil
	}
	srv, err := f.sourceFor(item)
	if err != nil {
		return "", err
	}
	key, ext := cacheKey(item)
	return f.cache.Get(ctx, key, ext, f.fill(srv, item))
}

// Stream is Local without the wait: a reader over the download as it arrives.
//
// Same fetch — same swarm-then-relay choice, same cache file, same cancellation
// — handed over while it runs rather than after it finishes. A track already
// cached comes back as an ordinary seekable file, so replaying one is unchanged.
//
// The extension travels with the reader because the decoders pick by it, and a
// file that is still being written has nothing else to go on.
func (f *Fetcher) Stream(ctx context.Context, item *queue.Item) (io.ReadCloser, string, error) {
	if item.Path != "" {
		rc, err := os.Open(item.Path)
		return rc, filepath.Ext(item.Path), err
	}
	srv, err := f.sourceFor(item)
	if err != nil {
		return nil, "", err
	}
	key, ext := cacheKey(item)
	rc, err := f.cache.Stream(ctx, key, ext, f.fill(srv, item))
	return rc, ext, err
}

// sourceFor is the server that can supply this item's bytes, or say who can.
//
// The two roles are different and the difference is the whole point of browsing
// the madnetwork from a player: for a library track the server IS the source and
// hands over the file; for a madnetwork track it is a directory that names
// holders, and the bytes come from them over the mesh.
func (f *Fetcher) sourceFor(item *queue.Item) (library.Server, error) {
	if item.Network {
		if item.Hash == "" || item.Base == "" {
			return library.Server{}, errors.New("this track has no audio to play")
		}
		return f.serverFor(item.Base)
	}
	if item.URL == "" {
		return library.Server{}, errors.New("this track has no audio to play")
	}
	return f.serverFor(item.URL)
}

// fill is the one fetch body both Local and Stream run, so the choice between
// the swarm and the relay is made in one place and cannot drift between "play
// this" and "keep this".
func (f *Fetcher) fill(srv library.Server, item *queue.Item) blobcache.Fetch {
	key, _ := cacheKey(item)
	return func(ctx context.Context, w io.Writer) error {
		// A fetch for a DIFFERENT track outranks the speculative one: a guess
		// must never make a person wait. Without this, a running prefetch held
		// the mesh slot for its whole transfer and a clicked track queued
		// behind it — bounded now (the slot wait shares the budget), but
		// "bounded" is still a budget's worth of silence for a guess. The
		// prefetch's own fill passes its own key and is not preempted by itself;
		// a playback fetch for the very track being prefetched joins that fetch
		// in the cache and never gets here.
		f.preemptPrefetch(key)
		wrote, declined, err := f.fromSwarm(ctx, srv, item, w)

		// A madnetwork track has no relay behind it, on purpose.
		//
		// There IS an endpoint that would serve one — /api/madnetwork/stream/
		// {hash} — and it is a cache-through relay: the server fetches somebody
		// else's audio, keeps a copy, and streams it on. That is the right shape
		// for a browser, which cannot join a swarm, and the wrong one here. This
		// device can fetch the bytes itself, and asking the home server to do it
		// instead would fill that machine's disk with the community's catalogue
		// as a side effect of somebody browsing it (owner's call, 2026-08-15:
		// the swarm supersedes the relay for network content).
		//
		// So the swarm is not preferred here, it is the whole path, and its
		// failure is the track's failure — said out loud rather than papered
		// over with a download the person did not ask anybody to make.
		if item.Network {
			switch {
			case err != nil:
				// Logged as well as reported: with no fallback underneath, this
				// line is the only account of why a track did not play.
				f.log.Printf("madplayer: madnetwork fetch of %s failed: %v", item.Hash, err)
				return fmt.Errorf("the madnetwork could not send this track: %w", err)
			case wrote == 0:
				f.log.Printf("madplayer: madnetwork fetch of %s declined: %s", item.Hash, declined)
				if declined == "" {
					declined = "nobody on the madnetwork sent anything"
				}
				return errors.New(declined)
			}
			return nil
		}

		switch {
		case err == nil && wrote > 0:
			return nil
		case wrote > 0 && ctx.Err() != nil:
			// Not a failure — an ABANDONMENT. Nobody is waiting for this track any
			// more (the queue moved on, and the last reader left), so there is
			// nothing to resume for. Saying "resuming" here would be a line about
			// work that is not happening, and the relay call would fail on the same
			// dead context anyway.
			return err
		case wrote > 0:
			// The swarm died PART WAY, and the bytes it managed are already in the
			// file. Starting the relay over from zero would append a second copy of
			// the prefix to the first and decode as noise, so it is asked for the
			// REST instead.
			//
			// That is sound because of two facts that hold together: the swarm
			// verifies each chunk before it is readable, so what landed is a correct
			// prefix, and a blob is addressed by its content hash, so the relay's
			// copy is byte-identical to the one the swarm was delivering. Resuming
			// splices two halves of the same file.
			f.log.Printf("madplayer: swarm fetch stopped after %d byte(s), resuming from %s: %v",
				wrote, srv.Label, err)
			return f.fromRelayAt(ctx, srv.Client, item, w, wrote)
		case err != nil:
			// Worth a line even though the relay below will probably succeed: a
			// swarm that quietly never works looks exactly like one that is working,
			// and nothing else in this program would ever say otherwise.
			f.log.Printf("madplayer: swarm fetch failed, falling back to %s: %v", srv.Label, err)
		}
		return f.fromRelay(ctx, srv.Client, item, w)
	}
}

// fromSwarm fetches from whoever holds the blob.
//
// The count reports whether anything reached w, and it is not the same question
// as the error: once a byte is written the relay must not be tried, whatever
// went wrong afterwards.
//
// Declining is ordinary and silent — no mesh on this device, a track named
// without a content hash, no enrolment with this server yet, nobody holding it.
// Each of those is answered by the relay, and none of them is news, which is why
// a decline is a nil error rather than a failure.
//
// It is news to ONE caller. A madnetwork track has no relay behind it, so a
// decline is the end of the road and the person is owed a reason — hence the
// middle return: a sentence naming which decline this was, empty whenever bytes
// moved or a real error is being reported. The relay path ignores it, which is
// the point: the reason exists for the path that cannot fall back.
func (f *Fetcher) fromSwarm(ctx context.Context, srv library.Server, item *queue.Item, w io.Writer) (int64, string, error) {
	f.mu.RLock()
	swarm, vouch := f.swarm, f.vouch
	f.mu.RUnlock()
	if swarm == nil || item.Hash == "" {
		return 0, "this device is not on the madnetwork", nil
	}

	// ONE deadline covers everything before bytes flow: taking the fetch slot,
	// asking who holds the track, and waiting for the first byte. They are one
	// question — "did the swarm answer?" — and giving each stage its own budget
	// quietly multiplied the worst-case silence by the number of stages.
	deadline := time.Now().Add(f.budget())

	// One fetch at a time, with the token installed inside the slot.
	//
	// A device signed in to several servers holds several tokens and the mesh
	// carries one: a third node checks the vouch against an issuer IT can place,
	// so the right one is from the server that named these holders
	// (docs/architecture/federation.md §"The household"). Presenting it and then
	// fetching has to be indivisible, or a prefetch for another server's track
	// swaps the token out from under a fetch that is already running.
	//
	// The wait for the slot is BOUNDED by the shared deadline: fetches stream
	// now, so whoever holds the slot may hold it for a whole transfer, and a
	// track that cannot have the mesh within its budget still has the relay.
	// Declining here is ordinary — the relay is a working answer, and silence
	// while queueing behind another download is not.
	select {
	case f.slot <- struct{}{}:
	case <-ctx.Done():
		return 0, "", ctx.Err()
	case <-time.After(time.Until(deadline)):
		return 0, "the madnetwork was busy with another track", nil
	}
	defer func() { <-f.slot }()
	if err := ctx.Err(); err != nil {
		return 0, "", err
	}
	if vouch != nil && !vouch.Present(srv.Base) {
		return 0, "this device has no vouch from " + srv.Label + " yet", nil
	}

	// The budget bounds the FIRST BYTE, not the whole transfer.
	//
	// It used to bound the whole thing, and the reasoning was sound at the time:
	// playback could not begin until the last byte landed, so a slow swarm meant
	// silence, and the relay had to be handed a live context while there was
	// still some of the caller's allowance left. Measured 2026-08-09, the relay
	// delivered a 20 MB track in 3.8s and the swarm took 4m05s.
	//
	// A track now plays from the bytes as they arrive, so a nineteen-second
	// transfer is not a nineteen-second wait — it is a track that started at
	// second one. Bounding the whole download therefore threw away working
	// fetches for no gain: measured 2026-08-15 against this device's own home
	// server, the swarm delivered 19.3 MiB in 18.9s and lost a 20s budget by a
	// second, every time, after spending the twenty seconds.
	//
	// What still deserves a deadline is a swarm that never answers, because that
	// IS silence. Expiring on the first byte is an ordinary decline: nothing has
	// been written, so the relay takes over with what the caller has left.

	// Who holds it is asked of the home server, every time.
	//
	// Since madplayer browses the madnetwork too (2026-08-15), some rows DO
	// arrive carrying versions[].holders[] — docs/design.md offers those as
	// a cheaper source, and they are still not used. Two reasons, and the second
	// is the load-bearing one: an ordinary library row has no holders at all, so
	// the endpoint would be needed anyway; and a browse row is as old as the
	// screen it is on, while the endpoint applies the stale-holder window that
	// makes a fetch plan worth having (federation.md §"Availability & node
	// health"). A dead holder in a plan is the most expensive thing that can
	// happen to a fetch, so freshness beats a saved round trip.
	// The holder lookup shares the deadline: it is part of "the swarm
	// answered", and a server that will not say who holds a track is a swarm
	// that never starts.
	hctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	plan, err := srv.Client.Holders(hctx, item.Hash)
	if err != nil {
		return 0, "", fmt.Errorf("asking %s who holds this track: %w", srv.Label, err)
	}
	keys := plan.Keys()
	if len(keys) == 0 {
		return 0, "nobody reachable has this track right now", nil
	}

	// Whatever the earlier stages left is the first-byte allowance. Nothing
	// left is a decline like any other — no byte was written.
	firstByte := time.Until(deadline)
	if firstByte <= 0 {
		return 0, "the madnetwork did not answer in time", nil
	}
	body, err := swarm.StreamBlob(ctx, item.Hash, plan.Size, keys, firstByte)
	if err != nil {
		return 0, "", fmt.Errorf("fetching from %d holder(s) of %s: %w", len(keys), item.Hash, err)
	}
	defer body.Close()

	// The count is what makes a mid-track failure recoverable: the caller resumes
	// the relay from exactly here (see fill). madshare verifies each chunk before
	// it is readable, so whatever io.Copy managed is a correct prefix rather than
	// an unknown amount of possibly-garbage.
	n, err := io.Copy(w, body)
	return n, "", err
}

// fromRelayAt downloads the REST of a track over the relay, picking up where
// something else stopped.
//
// The offset is a byte count of verified content, and the relay's copy of a
// content-addressed blob is the same bytes, so this splices two halves of one
// file rather than two files. A server that will not honour the range says so
// (madshare.OpenFrom refuses a 200) rather than quietly handing back the whole
// thing to be appended.
func (f *Fetcher) fromRelayAt(ctx context.Context, cl *madshare.Client, item *queue.Item, w io.Writer, offset int64) error {
	body, _, err := cl.OpenFrom(ctx, item.URL, offset)
	if err != nil {
		return describe(err, item)
	}
	defer body.Close()
	_, err = io.Copy(w, body)
	return err
}

// fromRelay downloads from the one server that named the track: level 1's
// playback path, unchanged and still the answer whenever the swarm declines.
func (f *Fetcher) fromRelay(ctx context.Context, cl *madshare.Client, item *queue.Item, w io.Writer) error {
	body, _, err := cl.Open(ctx, item.URL)
	if err != nil {
		return describe(err, item)
	}
	defer body.Close()
	_, err = io.Copy(w, body)
	return err
}

// TrackSize reports the item's total byte size, asked of its server once and
// remembered. The player's seek path needs it to turn a position in seconds
// into the byte offset a Range request wants.
func (f *Fetcher) TrackSize(ctx context.Context, item *queue.Item) (int64, error) {
	if item.URL == "" {
		return 0, errors.New("this track has no server to ask")
	}
	f.szmu.Lock()
	if n, ok := f.sizes[item.URL]; ok {
		f.szmu.Unlock()
		return n, nil
	}
	f.szmu.Unlock()
	srv, err := f.serverFor(item.URL)
	if err != nil {
		return 0, err
	}
	n, err := srv.Client.Length(ctx, item.URL)
	if err != nil {
		return 0, err
	}
	f.szmu.Lock()
	f.sizes[item.URL] = n
	f.szmu.Unlock()
	return n, nil
}

// OpenRange returns the track's bytes from a byte offset onward, over the
// relay — the exact mechanism the web UI seeks with, spelled as a client call.
// The server delivers the SEEKED region first by construction: /files/* serves
// any range of a finished file immediately, and the madnetwork streaming relay
// answers a range by prioritizing that chunk of its own swarm fetch.
//
// This is a LISTENING surface, not a caching one: whatever background fill is
// running for the track keeps running and keeps owning the cache file — bytes
// read here land in the decoder and nowhere else.
func (f *Fetcher) OpenRange(ctx context.Context, item *queue.Item, offset int64) (io.ReadCloser, error) {
	srv, err := f.serverFor(item.URL)
	if err != nil {
		return nil, err
	}
	body, _, err := srv.Client.OpenFrom(ctx, item.URL, offset)
	if err != nil {
		return nil, describe(err, item)
	}
	return body, nil
}

// Cached reports whether the item can be played with no network at all.
func (f *Fetcher) Cached(item *queue.Item) bool {
	if item.Path != "" {
		return true
	}
	if !item.Playable() {
		return false
	}
	key, ext := cacheKey(item)
	_, ok := f.cache.Lookup(key, ext)
	return ok
}

// Prefetch starts downloading a track that is not playing yet — the next one in
// the queue — so the gap between tracks is not a download.
//
// Only one runs at a time, and asking for a different one abandons the last.
// A track already on disk costs nothing here, which is what makes it safe to
// call on every change the player reports.
func (f *Fetcher) Prefetch(item *queue.Item) {
	if item == nil || !item.Remote() {
		return
	}
	key, ext := cacheKey(item)

	f.pmu.Lock()
	if f.prefetchKey == key {
		f.pmu.Unlock()
		return // already running for this very track
	}
	// Whatever else is running is a STALE guess — the queue moved and this
	// track is the guess now — so it stops even when the new guess turns out
	// to be on disk already. The cached check used to come first, which left
	// the stale download running (and holding the mesh slot) with nothing to
	// preempt it until the next real fetch.
	if f.prefetchStop != nil {
		f.prefetchStop()
		f.prefetchStop = nil
		f.prefetchKey = ""
	}
	if _, ok := f.cache.Lookup(key, ext); ok {
		f.pmu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	f.prefetchKey, f.prefetchStop = key, cancel
	f.pmu.Unlock()

	go func() {
		defer cancel()
		// The result is deliberately discarded: a prefetch that fails is not an
		// error anybody asked about, and playing the track will report it
		// properly if it is still broken then.
		_, _ = f.Local(ctx, item)
		f.pmu.Lock()
		if f.prefetchKey == key {
			f.prefetchKey = ""
		}
		f.pmu.Unlock()
	}()
}

// preemptPrefetch abandons a running prefetch when a fetch for a DIFFERENT
// track starts: the slot is single, transfers are long, and a guess about the
// future must never be what an actual request waits behind. Reached from fill,
// so every real fetch — playback, keep-on-device — outranks the speculation.
//
// Deliberately NOT reached when the keys match: the prefetch of this very
// track IS the fetch (the cache joins them), and cancelling it would cancel
// the caller.
func (f *Fetcher) preemptPrefetch(key string) {
	f.pmu.Lock()
	defer f.pmu.Unlock()
	if f.prefetchKey == "" || f.prefetchKey == key || f.prefetchStop == nil {
		return
	}
	f.prefetchStop()
	f.prefetchStop = nil
	f.prefetchKey = ""
}

// StopPrefetch abandons any speculative download. Called on the way out.
func (f *Fetcher) StopPrefetch() {
	f.pmu.Lock()
	if f.prefetchStop != nil {
		f.prefetchStop()
		f.prefetchStop = nil
	}
	f.prefetchKey = ""
	f.pmu.Unlock()
}

// serverFor finds the server a URL belongs to.
//
// The whole server is returned rather than its client, because a mesh fetch
// needs its base as well: the vouch it presents is the one THAT server issued,
// and which server a track came from is the only thing that decides it.
//
// The longest matching base wins, so a server reached at host/madshare is not
// shadowed by one at host — which is a real deployment, since madshare is
// commonly behind a path on a reverse proxy.
func (f *Fetcher) serverFor(u string) (library.Server, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()

	var best library.Server
	for _, s := range f.servers {
		if s.Client == nil || !strings.HasPrefix(u, s.Base) {
			continue
		}
		if len(s.Base) > len(best.Base) {
			best = s
		}
	}
	if best.Client == nil {
		return library.Server{}, errors.New("this track is on a server this device is no longer signed in to")
	}
	return best, nil
}

// cacheKey names the file on disk.
//
// A content hash is preferred over the URL, so the same audio offered by two
// servers is stored once — and so a server changing address does not orphan
// everything already downloaded from it.
func cacheKey(item *queue.Item) (key, ext string) {
	ext = filepath.Ext(library.FileName(item.URL))
	if ext == "" && item.Codec != "" {
		// A madnetwork copy has no filename anywhere — the catalogue names a
		// content hash, a size and a codec. The extension is not decoration: the
		// decoders pick by it, so a cache file written without one is audio
		// nothing in this program can open.
		ext = "." + strings.TrimPrefix(strings.ToLower(item.Codec), ".")
	}
	if item.Hash != "" {
		return blobcache.Key(item.Hash), ext
	}
	return blobcache.Key(item.URL), ext
}

// describe turns a server's refusal into something worth reading. A revoked
// token and a deleted track are different problems with different answers, and
// "404" tells the person neither.
func describe(err error, item *queue.Item) error {
	var e *madshare.Error
	if !errors.As(err, &e) {
		return err
	}
	where := item.Origin
	if where == "" {
		where = "the server"
	}
	switch e.Status {
	case 401:
		return fmt.Errorf("%s no longer accepts this device's sign-in — sign in again", where)
	case 403:
		return fmt.Errorf("your account on %s may not play this track", where)
	case 404:
		return fmt.Errorf("%s no longer has this track", where)
	}
	return err
}
