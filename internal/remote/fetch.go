// Package remote downloads tracks that live on somebody else's madshare and
// hands the player a local file.
//
// It is the join between three things that deliberately know nothing about each
// other: the queue (which carries a URL), the HTTP client (which knows the
// credential for one server) and the cache (which knows what is already on
// disk). The player asks for a path and gets one; that a download happened is
// not its business.
package remote

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"

	"daemonlord.ygg/madplayer/internal/blobcache"
	"daemonlord.ygg/madplayer/internal/library"
	"daemonlord.ygg/madplayer/internal/madshare"
	"daemonlord.ygg/madplayer/internal/queue"
)

// Fetcher satisfies player.Fetcher.
type Fetcher struct {
	cache *blobcache.Cache

	mu      sync.RWMutex
	servers []library.Server

	// prefetch is the speculative download of the NEXT track, cancelled as soon
	// as a different one is wanted. Without the cancel, changing your mind about
	// what to play next would leave the old guess downloading.
	pmu          sync.Mutex
	prefetchKey  string
	prefetchStop context.CancelFunc
}

// New returns a fetcher over a cache.
func New(cache *blobcache.Cache) *Fetcher { return &Fetcher{cache: cache} }

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
	cl, err := f.clientFor(item.URL)
	if err != nil {
		return "", err
	}
	key, ext := cacheKey(item)
	return f.cache.Get(ctx, key, ext, func(ctx context.Context, w io.Writer) error {
		body, _, err := cl.Open(ctx, item.URL)
		if err != nil {
			return describe(err, item)
		}
		defer body.Close()
		_, err = io.Copy(w, body)
		return err
	})
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

// clientFor finds the server a URL belongs to.
//
// The longest matching base wins, so a server reached at host/madshare is not
// shadowed by one at host — which is a real deployment, since madshare is
// commonly behind a path on a reverse proxy.
func (f *Fetcher) clientFor(u string) (*madshare.Client, error) {
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
		return nil, errors.New("this track is on a server this device is no longer signed in to")
	}
	return best.Client, nil
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
