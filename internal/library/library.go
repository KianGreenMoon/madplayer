// Package library is the browse view over every library this client can reach:
// the embedded backend on this device, plus each madshare it is signed in to.
//
// It holds no index and resolves no entities. Both used to live here, ported from
// the server, because the backend was not embedded yet; now the backend IS the
// library engine and this package only fetches what it decides and shapes it for
// the screen. Which names are artists, what belongs to an album, what order any
// of it comes in, and which file a track plays are all answered in SQL — see
// docs/ui/madplayer.md §"What the server already computes", which is also the
// list of things a client must never re-derive.
//
// What is left here is genuinely the client's: the shape the widgets read, the
// local path a decoder can open, the disc headers (a display rule the server has
// no opinion about — see disc.go), and the MERGE of several libraries into one
// list, which no single server can do because none of them can see the others
// (see merge.go, which follows the server's own cross-node identity rule).
package library

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"

	"daemonlord.ygg/madshare/app"
	"daemonlord.ygg/madshare/database"

	"daemonlord.ygg/madplayer/internal/madshare"
)

// DeviceID is the source id of the library on this machine. It is the empty
// string so that a zero Origin means "here", which is the common case.
const DeviceID = ""

// Origin is one library a row appears in, and the id it has THERE. Ids are
// per-library: two servers both call something 41, and they are not the same
// thing, so nothing may be addressed by an id without the source beside it.
type Origin struct {
	Source string // DeviceID, or the server's base URL
	Label  string // what to call it on screen
	ID     int64
}

// OnDevice reports whether this origin is the library on this machine.
func (o Origin) OnDevice() bool { return o.Source == DeviceID }

// Source is one library that can be browsed. Both the embedded backend and a
// remote server implement it, and the merge above does not care which is which
// — that is the whole point of the interface.
type Source interface {
	ID() string
	Label() string
	Artists(ctx context.Context) ([]*Artist, error)
	Albums(ctx context.Context, artistID int64) ([]*Album, error)
	AlbumTracks(ctx context.Context, albumID int64, albumTitle string) ([]*Track, error)
	Search(ctx context.Context, q string) (SearchResults, error)
}

// Problem is one library that could not be read.
//
// It is reported alongside the rows rather than instead of them: a server being
// down must never blank the music on this device, which is the whole argument
// for the player working with no network at all.
type Problem struct {
	Label string
	Err   error
}

func (p Problem) Error() string { return p.Label + ": " + p.Err.Error() }

// Artist is a row of the artist list: album artists, merged across libraries.
type Artist struct {
	Name string
	// TrackCount is a LOWER BOUND once Approx is set — see merge.go countFrom.
	TrackCount int
	Approx     bool
	Origins    []Origin
}

// Album belongs to one album-artist, and may exist in several libraries.
type Album struct {
	// ArtistName is the album ARTIST — what a breadcrumb should say when an album
	// is opened straight from a search hit, with no artist level walked through.
	ArtistName string
	// ArtistOrigins is that artist's own ids, so the breadcrumb it fills in is a
	// working link and not just a name. Without it, an album reached from a
	// search hit has an artist crumb that leads nowhere.
	ArtistOrigins []Origin

	Title      string
	Year       int
	TrackCount int
	Approx     bool
	Origins    []Origin
}

// Artist rebuilds the album-artist a breadcrumb needs.
func (al *Album) Artist() *Artist {
	return &Artist{Name: al.ArtistName, Origins: al.ArtistOrigins}
}

// Copy is one library's copy of a track — where its bytes are, and how to get
// at them.
//
// Origin.ID is the copy's identity in ITS library: the tagset, the appearance,
// which is what favourites, playlists and the renditions endpoint key on there.
type Copy struct {
	Origin Origin

	// Path is the file on this machine, and is empty when nothing here holds it:
	// an unplugged drive, an ejected card, a folder somebody moved. On a server
	// that is an incident; on a player it is Tuesday.
	Path string
	// URL is the absolute play address on a remote server.
	URL string
	// Hash is the content hash of the rendition that actually plays, which is
	// what makes it usable as a cache key: the same audio fetched from two
	// servers is stored once.
	Hash string
	MIME string
}

// Track is one logical track: the same recording may be held by several
// libraries, and every copy is kept.
type Track struct {
	Title  string // never empty: the server falls back to the filename
	Artist string // the PERFORMER — the track's own credit, not the album artist
	Album  string

	TrackNumber int
	// DiscNumber is nil when untagged. Untagged, 0 and N are three DISTINCT
	// discs, which is why this is a pointer (see disc.go).
	DiscNumber *int

	// Duration in seconds; 0 means "not known".
	Duration float64

	Copies []Copy
}

// Best is the copy to play: this device's bytes when it has them, and otherwise
// the first library that does.
//
// Preferring local is not an optimisation, it is the offline case working — a
// track this machine holds must play with the network unplugged, whichever
// server also happens to have it.
func (t *Track) Best() (Copy, bool) {
	for _, c := range t.Copies {
		if c.Path != "" {
			return c, true
		}
	}
	for _, c := range t.Copies {
		if c.URL != "" {
			return c, true
		}
	}
	return Copy{}, false
}

// LocalPath is the file on this machine, or "" — what a decoder can open
// without asking anybody.
func (t *Track) LocalPath() string {
	for _, c := range t.Copies {
		if c.Path != "" {
			return c.Path
		}
	}
	return ""
}

// Available reports whether anything can currently play this track.
func (t *Track) Available() bool { _, ok := t.Best(); return ok }

// Remote reports that the only copies are on other machines, so playing this
// costs a download.
func (t *Track) Remote() bool { return t.Available() && t.LocalPath() == "" }

// SearchResults mirrors the server's three-section shape.
type SearchResults struct {
	Artists []*Artist
	Albums  []*Album
	Tracks  []*Track
}

// Empty reports whether a search found nothing at all.
func (r SearchResults) Empty() bool {
	return len(r.Artists) == 0 && len(r.Albums) == 0 && len(r.Tracks) == 0
}

// Library is the merged browse view. The device library is always source zero,
// so it is the first to answer and the first whose values win a tie.
type Library struct {
	mu      sync.RWMutex
	device  Source
	remotes []Source
}

// New wraps the embedded backend's browse surface. Servers are added afterwards
// with SetServers.
func New(src app.Library) *Library {
	return &Library{device: deviceSource{lib: src}}
}

// Server is a signed-in remote madshare.
type Server struct {
	Base   string
	Label  string
	Client *madshare.Client
}

// SetServers replaces the remote libraries. Passing none leaves the device
// library alone, which is the offline player it always was.
func (l *Library) SetServers(servers []Server) {
	remotes := make([]Source, 0, len(servers))
	for _, s := range servers {
		remotes = append(remotes, remoteSource{base: s.Base, label: s.Label, cl: s.Client})
	}
	l.mu.Lock()
	l.remotes = remotes
	l.mu.Unlock()
}

// sources is the fan-out order: this device, then the servers as configured.
func (l *Library) sources() []Source {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]Source, 0, 1+len(l.remotes))
	if l.device != nil {
		out = append(out, l.device)
	}
	return append(out, l.remotes...)
}

// Remote reports whether any server is configured — the switch between "a music
// player" and "a music player plus somebody else's library".
func (l *Library) Remote() bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return len(l.remotes) > 0
}

// Artists is the browse list: album artists only, merged, already ordered, with
// the Unknown bucket last. A name that only ever performs on other people's
// releases is a SEARCH HIT and not a row — the rule, and the one way to get it
// wrong, are in docs/ui/artists-and-performers.md.
func (l *Library) Artists(ctx context.Context) ([]*Artist, []Problem, error) {
	lists, probs, err := fanOut(ctx, l.sources(), func(ctx context.Context, s Source) ([]*Artist, error) {
		return s.Artists(ctx)
	})
	if err != nil {
		return nil, probs, err
	}
	return mergeArtists(lists), probs, nil
}

// Albums returns one merged artist's albums, from every library that has them.
func (l *Library) Albums(ctx context.Context, ar *Artist) ([]*Album, []Problem, error) {
	if ar == nil {
		return nil, nil, nil
	}
	lists, probs, err := perOrigin(ctx, l.sources(), ar.Origins, func(ctx context.Context, s Source, id int64) ([]*Album, error) {
		return s.Albums(ctx, id)
	})
	if err != nil {
		return nil, probs, err
	}
	return mergeAlbums(lists), probs, nil
}

// AlbumTracks returns an album's tracks in listening order — the WHOLE album,
// whichever artist the caller drilled in from, and whichever libraries hold it.
func (l *Library) AlbumTracks(ctx context.Context, al *Album) ([]*Track, []Problem, error) {
	if al == nil {
		return nil, nil, nil
	}
	lists, probs, err := perOrigin(ctx, l.sources(), al.Origins, func(ctx context.Context, s Source, id int64) ([]*Track, error) {
		return s.AlbumTracks(ctx, id, al.Title)
	})
	if err != nil {
		return nil, probs, err
	}
	return mergeTracks(lists), probs, nil
}

// Search matches artists in both credit roles, album titles and track titles,
// across every library.
//
// Matching both roles is what keeps the album-artists-only browse list a display
// rule rather than missing data: a performer with no release of their own is
// unreachable by browsing, and search is the path to them.
func (l *Library) Search(ctx context.Context, q string) (SearchResults, []Problem, error) {
	lists, probs, err := fanOut(ctx, l.sources(), func(ctx context.Context, s Source) ([]SearchResults, error) {
		res, err := s.Search(ctx, q)
		return []SearchResults{res}, err
	})
	if err != nil {
		return SearchResults{}, probs, err
	}
	var artists [][]*Artist
	var albums [][]*Album
	var tracks [][]*Track
	for _, one := range lists {
		for _, res := range one {
			artists = append(artists, res.Artists)
			albums = append(albums, res.Albums)
			tracks = append(tracks, res.Tracks)
		}
	}
	return SearchResults{
		Artists: mergeArtists(artists),
		Albums:  mergeAlbums(albums),
		Tracks:  mergeTracks(tracks),
	}, probs, nil
}

// perOrigin runs a call against exactly the libraries a merged row came from,
// with the id it has in each. Origins naming a source that has since gone are
// skipped rather than failed: signing out of a server while its albums are on
// screen is a normal thing to do.
func perOrigin[T any](ctx context.Context, sources []Source, origins []Origin,
	call func(context.Context, Source, int64) ([]T, error)) ([][]T, []Problem, error) {

	byID := map[string]Source{}
	for _, s := range sources {
		byID[s.ID()] = s
	}
	type job struct {
		src Source
		id  int64
	}
	jobs := make([]job, 0, len(origins))
	for _, o := range origins {
		if s, ok := byID[o.Source]; ok {
			jobs = append(jobs, job{src: s, id: o.ID})
		}
	}
	return gather(ctx, len(jobs), func(i int) (string, func(context.Context) ([]T, error)) {
		j := jobs[i]
		return j.src.Label(), func(ctx context.Context) ([]T, error) { return call(ctx, j.src, j.id) }
	})
}

// fanOut runs a call against every library at once.
func fanOut[T any](ctx context.Context, sources []Source,
	call func(context.Context, Source) ([]T, error)) ([][]T, []Problem, error) {

	return gather(ctx, len(sources), func(i int) (string, func(context.Context) ([]T, error)) {
		s := sources[i]
		return s.Label(), func(ctx context.Context) ([]T, error) { return call(ctx, s) }
	})
}

// gather runs n calls in parallel and keeps their order.
//
// A failure is a Problem and not an error: one unreachable server must not blank
// the rest of the music. Only when EVERY library failed is there nothing to show
// and a real error to report — an empty list would otherwise say "you own
// nothing", which is a much worse lie than "that server did not answer".
func gather[T any](ctx context.Context, n int,
	at func(int) (string, func(context.Context) ([]T, error))) ([][]T, []Problem, error) {

	lists := make([][]T, n)
	errs := make([]error, n)
	labels := make([]string, n)

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		label, run := at(i)
		labels[i] = label
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			lists[i], errs[i] = run(ctx)
		}(i)
	}
	wg.Wait()

	out := make([][]T, 0, n)
	var probs []Problem
	failed := 0
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			failed++
			probs = append(probs, Problem{Label: labels[i], Err: errs[i]})
			continue
		}
		out = append(out, lists[i])
	}
	if n > 0 && failed == n {
		joined := make([]error, 0, len(probs))
		for _, p := range probs {
			joined = append(joined, fmt.Errorf("%s: %w", p.Label, p.Err))
		}
		return nil, probs, errors.Join(joined...)
	}
	return out, probs, nil
}

// Renditions lists the other files of a track's recording, best first — the
// quality picker's list, where index 0 is what Auto resolves to. Only the device
// library answers it: the picker is the one place the client overrides which
// file plays, and overriding a choice made on somebody else's server is a
// different feature.
func (l *Library) Renditions(ctx context.Context, tagsetID int64) ([]database.DuplicateRendition, error) {
	l.mu.RLock()
	dev := l.device
	l.mu.RUnlock()
	d, ok := dev.(deviceSource)
	if !ok {
		return nil, nil
	}
	return d.lib.Renditions(ctx, tagsetID)
}

// discOf maps a nullable disc number to the pointer the disc rule needs. A NULL
// and a 0 must stay distinguishable, which a plain int cannot do.
func discOf(n sql.NullInt64) *int {
	if !n.Valid {
		return nil
	}
	v := int(n.Int64)
	return &v
}
