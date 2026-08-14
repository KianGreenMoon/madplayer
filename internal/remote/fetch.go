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
// the swarm has to decline ends here (docs/ui/madplayer.md §"Level 2b,
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
	FetchBlob(ctx context.Context, hash string, size int64, holders []string) (io.ReadCloser, error)
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

	// smu serializes mesh fetches. The mesh carries ONE vouch, installed
	// process-wide, so a second fetch starting underneath the first would present
	// its own token and break it — see fromSwarm.
	smu sync.Mutex

	// prefetch is the speculative download of the NEXT track, cancelled as soon
	// as a different one is wanted. Without the cancel, changing your mind about
	// what to play next would leave the old guess downloading.
	pmu          sync.Mutex
	prefetchKey  string
	prefetchStop context.CancelFunc
}

// New returns a fetcher over a cache. It fetches over the relay until SetSwarm
// says this device is on the mesh.
func New(cache *blobcache.Cache, lg *log.Logger) *Fetcher {
	if lg == nil {
		lg = log.Default()
	}
	return &Fetcher{cache: cache, log: lg}
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
	if item.URL == "" {
		return "", errors.New("this track has no audio to play")
	}
	srv, err := f.serverFor(item.URL)
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
	if item.URL == "" {
		return nil, "", errors.New("this track has no audio to play")
	}
	srv, err := f.serverFor(item.URL)
	if err != nil {
		return nil, "", err
	}
	key, ext := cacheKey(item)
	rc, err := f.cache.Stream(ctx, key, ext, f.fill(srv, item))
	return rc, ext, err
}

// fill is the one fetch body both Local and Stream run, so the choice between
// the swarm and the relay is made in one place and cannot drift between "play
// this" and "keep this".
func (f *Fetcher) fill(srv library.Server, item *queue.Item) blobcache.Fetch {
	return func(ctx context.Context, w io.Writer) error {
		wrote, err := f.fromSwarm(ctx, srv, item, w)
		switch {
		case wrote && err == nil:
			return nil
		case wrote:
			// Bytes are already in the file. Retrying over the relay would append a
			// second source's copy to the first's and produce something that decodes
			// as noise, so this attempt is the only one.
			return err
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
// The bool reports whether anything reached w, and it is not the same question
// as the error: once a byte is written the relay must not be tried, whatever
// went wrong afterwards.
//
// Declining is ordinary and silent — no mesh on this device, a track named
// without a content hash, no enrolment with this server yet, nobody holding it.
// Each of those is answered by the relay, and none of them is news.
func (f *Fetcher) fromSwarm(ctx context.Context, srv library.Server, item *queue.Item, w io.Writer) (bool, error) {
	f.mu.RLock()
	swarm, vouch := f.swarm, f.vouch
	f.mu.RUnlock()
	if swarm == nil || item.Hash == "" {
		return false, nil
	}

	// One fetch at a time, with the token installed inside the lock.
	//
	// A device signed in to several servers holds several tokens and the mesh
	// carries one: a third node checks the vouch against an issuer IT can place,
	// so the right one is from the server that named these holders
	// (docs/architecture/federation.md §"The household"). Presenting it and then
	// fetching has to be indivisible, or a prefetch for another server's track
	// swaps the token out from under a fetch that is already running.
	//
	// The cost is that a slow swarm fetch delays the next one rather than running
	// beside it. That is the right trade here: the queue fetches the playing track
	// and speculatively the one after it, so the thing being made to wait is a
	// guess about the future, and it inherits a context that is cancelled the
	// moment the guess turns out wrong.
	f.smu.Lock()
	defer f.smu.Unlock()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if vouch != nil && !vouch.Present(srv.Base) {
		return false, nil
	}

	// The swarm gets a BUDGET, not the caller's whole deadline.
	//
	// Without this the faster path can starve the one that works: measured against
	// a real server (2026-08-09), the relay delivered a 20 MB track in 3.8s and
	// the swarm took 4m05s for the same bytes — so a swarm attempt left to run to
	// the caller's deadline spends the entire allowance and then hands the relay a
	// context that is already dead. The person gets no music at all, which is
	// strictly worse than the level-1 client they had before.
	//
	// Expiring here is an ordinary decline: nothing has been written, so the relay
	// takes over with whatever the caller still has left.
	sctx, cancel := context.WithTimeout(ctx, f.budget())
	defer cancel()

	// Who holds it is asked of the home server, every time.
	//
	// docs/ui/madplayer.md offers a browse row's own versions[].holders[] as the
	// cheaper source, and this client never has one: those rows belong to the
	// /madnetwork page, which browses OTHER nodes' catalogs, while madplayer
	// merges each server's ordinary library — and an ordinary track row carries no
	// holders. So the endpoint is the only source here, not the fallback.
	plan, err := srv.Client.Holders(sctx, item.Hash)
	if err != nil {
		return false, fmt.Errorf("asking %s who holds this track: %w", srv.Label, err)
	}
	keys := plan.Keys()
	if len(keys) == 0 {
		return false, nil
	}

	body, err := swarm.FetchBlob(sctx, item.Hash, plan.Size, keys)
	if err != nil {
		return false, fmt.Errorf("fetching from %d holder(s) of %s: %w", len(keys), item.Hash, err)
	}
	defer body.Close()
	n, err := io.Copy(w, body)
	return n > 0, err
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

// Cached reports whether the item can be played with no network at all.
func (f *Fetcher) Cached(item *queue.Item) bool {
	if item.Path != "" {
		return true
	}
	if item.URL == "" {
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
	if _, ok := f.cache.Lookup(key, ext); ok {
		return
	}

	f.pmu.Lock()
	if f.prefetchKey == key {
		f.pmu.Unlock()
		return // already running for this very track
	}
	if f.prefetchStop != nil {
		f.prefetchStop()
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
