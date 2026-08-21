// Package library is the browse view over every library this client can reach:
// the embedded backend on this device, plus each madshare it is signed in to.
//
// It holds no index and resolves no entities. Both used to live here, ported from
// the server, because the backend was not embedded yet; now the backend IS the
// library engine and this package only fetches what it decides and shapes it for
// the screen. Which names are artists, what belongs to an album, what order any
// of it comes in, and which file a track plays are all answered in SQL — see
// madshare's docs/ui/madplayer.md §"What the server already computes", which is
// also the list of things a client must never re-derive.
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
	"path/filepath"
	"strings"
	"sync"

	"daemonlord.ygg/madshare/app"
	"daemonlord.ygg/madshare/database"

	"daemonlord.ygg/madplayer/internal/madshare"
)

// DeviceID is the source id of the library on this machine. It is the empty
// string so that a zero Origin means "here", which is the common case.
const DeviceID = ""

// Origin is one library a row appears in, and the address it has THERE.
// Addresses are per-library: two servers both call something 41, and they are
// not the same thing, so nothing may be addressed without the source beside it.
//
// There are two kinds of address because there are two kinds of library. A
// library with an index addresses a row by ID. The madnetwork has no id space
// at all — it is many nodes' catalogs merged on text, and the ids in it belong
// to the nodes it was merged from — so it addresses by Ref, the name the server
// itself takes as a query parameter. A source uses whichever it filled in.
type Origin struct {
	Source string // DeviceID, or the server's base URL
	Label  string // what to call it on screen
	ID     int64
	// Ref is the name-shaped address, for a source that has no ids: the artist
	// name on an artist row, and the ALBUM ARTIST's name on an album row, which
	// with the album's own title is what addresses its tracks.
	Ref string
}

// OnDevice reports whether this origin is the library on this machine.
func (o Origin) OnDevice() bool { return o.Source == DeviceID }

// Source is one library that can be browsed. Both the embedded backend and a
// remote server implement it, and the merge above does not care which is which
// — that is the whole point of the interface.
//
// Drilling takes the whole Origin rather than an id, because what addresses a
// row differs by source (see Origin) and only the source that produced one
// knows which half of it to read.
type Source interface {
	ID() string
	Label() string
	Artists(ctx context.Context) ([]*Artist, error)
	Albums(ctx context.Context, artist Origin) ([]*Album, error)
	AlbumTracks(ctx context.Context, album Origin, albumTitle string) ([]*Track, error)
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

// errNoClient is a server row with no HTTP client behind it — a wiring mistake,
// answered rather than dereferenced. See remoteSource.client.
var errNoClient = errors.New("this server has no connection configured")

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

	// Size is the rendition's byte length, and Codec the container it is in
	// ("mp3", "flac"). Both are filled in only by the madnetwork, and both are
	// needed there for the same reason: a copy nobody named a file for has no
	// name to take a length or an extension from.
	Size  int64
	Codec string

	// Network marks a copy that exists on the mesh and NOWHERE this client can
	// address with a URL. Playing it means fetching the hash from whoever holds
	// it, through the server named by Origin.Source — which supplies the holder
	// list and no bytes.
	//
	// The three kinds of copy are exhaustive and each names its own way to the
	// audio: a Path is opened, a URL is downloaded, a Network hash is swarmed.
	Network bool
}

// Playable reports whether this copy names audio that can be reached at all.
func (c Copy) Playable() bool { return c.Path != "" || c.URL != "" || (c.Network && c.Hash != "") }

// Ext is the file extension for these bytes, with the dot.
//
// It matters more than a file name usually does, because the decoders pick by it
// and nothing else: a cached download written without one is audio this program
// cannot open. A madnetwork copy has no filename anywhere — the catalogue names
// a hash, a size and a codec — so the codec is where its extension comes from.
func (c Copy) Ext() string {
	if c.Path != "" {
		return filepath.Ext(c.Path)
	}
	if e := filepath.Ext(FileName(c.URL)); e != "" {
		return e
	}
	if c.Codec != "" {
		return extForCodec(c.Codec)
	}
	return ""
}

// extForCodec turns a codec name into a file extension.
//
// A catalogue row's codec is what ffprobe called the STREAM, and several of
// those names are not extensions anybody uses: a 24-bit WAV reports
// `pcm_s24le`, and a file written as `.pcm_s24le` is one no decoder here opens
// and no scanner indexes — it would land in the managed folder as something
// between a stray and a mystery. The names that already are extensions (mp3,
// flac, opus) fall through unchanged, which is most of them.
func extForCodec(codec string) string {
	c := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(codec)), ".")
	if c == "" {
		return ""
	}
	if strings.HasPrefix(c, "pcm_") || c == "adpcm" {
		return ".wav"
	}
	switch c {
	case "vorbis":
		return ".ogg"
	case "aac", "alac", "mp4a":
		return ".m4a"
	case "mp2":
		return ".mp3"
	}
	return "." + c
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

// Best is the copy to play: this device's bytes when it has them, then a server
// that can hand them over directly, and only then the mesh.
//
// Preferring local is not an optimisation, it is the offline case working — a
// track this machine holds must play with the network unplugged, whichever
// server also happens to have it. The madnetwork comes last for the neighbouring
// reason: a copy a signed-in server holds is one HTTP request, while a copy only
// the mesh has costs a holder lookup and a swarm. When the same audio is both,
// it is the same bytes either way, so the cheap route wins.
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
	for _, c := range t.Copies {
		if c.Network && c.Hash != "" {
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

// Scope is how much of what this device can reach is being browsed.
//
// The default is everything, and that is the posture: one list holding this
// machine's music, each signed-in server's library, and the madnetwork those
// servers can see, merged by the server's own identity rule. A row's origin
// badge says where it came from; the list does not ask you to choose first.
//
// ScopeDevice is the one deliberate narrowing, for the moment when "what is
// actually HERE" is the question — before a flight, on a metered connection, or
// simply to see the collection rather than the network. It is not the offline
// mode: an unreachable server already drops out of ScopeAll on its own, with a
// note beside the rows rather than instead of them.
type Scope int

const (
	// ScopeAll is this device, the servers, and the madnetwork through them.
	ScopeAll Scope = iota
	// ScopeDevice is only what this machine holds.
	ScopeDevice
)

// Library is the merged browse view. The device library is always source zero,
// so it is the first to answer and the first whose values win a tie.
type Library struct {
	mu      sync.RWMutex
	device  Source
	remotes []Source
	network []Source
	scope   Scope
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
//
// Each server contributes TWO sources: its own library, and the madnetwork it
// can see. They are separate because they fail separately — an account without
// `madnetwork.access` gets the library and a forbidden network, and that must
// read as one library answering rather than as the server being down.
func (l *Library) SetServers(servers []Server) {
	remotes := make([]Source, 0, len(servers))
	network := make([]Source, 0, len(servers))
	for _, s := range servers {
		remotes = append(remotes, remoteSource{base: s.Base, label: s.Label, cl: s.Client})
		network = append(network, madnetworkSource{base: s.Base, label: madnetworkLabel(s.Label), cl: s.Client})
	}
	l.mu.Lock()
	l.remotes, l.network = remotes, network
	l.mu.Unlock()
}

// madnetworkLabel is what a row from the community catalogue says it came from.
//
// It names the network rather than the server, because that is what is true: the
// bytes come from whoever holds them, and the server only knew where to look.
// Saying the server's name would credit it with content it does not have.
const madnetworkName = "madnetwork"

func madnetworkLabel(string) string { return madnetworkName }

// SetScope narrows or widens what is browsed. It takes effect on the next fetch,
// which is what the caller does after changing it.
func (l *Library) SetScope(s Scope) {
	l.mu.Lock()
	l.scope = s
	l.mu.Unlock()
}

// Scope reports what is being browsed.
func (l *Library) Scope() Scope {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.scope
}

// sources is the fan-out order: this device, then the servers as configured,
// then the madnetwork through each of them. Device-first is what makes a local
// value win a tie in the merge; the network is last for the same reason in
// reverse — it is the least specific claim about a row.
func (l *Library) sources() []Source {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]Source, 0, 1+len(l.remotes)+len(l.network))
	if l.device != nil {
		out = append(out, l.device)
	}
	if l.scope == ScopeDevice {
		return out
	}
	out = append(out, l.remotes...)
	return append(out, l.network...)
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
	lists, probs, err := perOrigin(ctx, l.sources(), ar.Origins, func(ctx context.Context, s Source, o Origin) ([]*Album, error) {
		return s.Albums(ctx, o)
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
	lists, probs, err := perOrigin(ctx, l.sources(), al.Origins, func(ctx context.Context, s Source, o Origin) ([]*Track, error) {
		return s.AlbumTracks(ctx, o, al.Title)
	})
	if err != nil {
		return nil, probs, err
	}
	return mergeTracks(lists), probs, nil
}

// DeviceAlbumTracks is AlbumTracks restricted to the library on THIS machine.
//
// It exists for one caller — cover art in the album list — and the restriction
// is the point. A cover is read out of an audio file, so it needs a path, and
// only a track carries one; asking every signed-in server for the tracks of
// every album on screen would turn scrolling a list into a fan-out. The device
// library answers from SQLite in microseconds, and an album this machine does
// not hold has no cover here, which is the truth rather than a limitation.
//
// A missing device origin is not an error: it is an album that lives only on a
// server.
func (l *Library) DeviceAlbumTracks(ctx context.Context, al *Album) ([]*Track, error) {
	l.mu.RLock()
	device := l.device
	l.mu.RUnlock()
	if al == nil || device == nil {
		return nil, nil
	}
	for _, o := range al.Origins {
		if o.OnDevice() {
			return device.AlbumTracks(ctx, o, al.Title)
		}
	}
	return nil, nil
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
	call func(context.Context, Source, Origin) ([]T, error)) ([][]T, []Problem, error) {

	byID := map[string]Source{}
	for _, s := range sources {
		byID[s.ID()] = s
	}
	type job struct {
		src Source
		at  Origin
	}
	jobs := make([]job, 0, len(origins))
	for _, o := range origins {
		if s, ok := byID[o.Source]; ok {
			jobs = append(jobs, job{src: s, at: o})
		}
	}
	return gather(ctx, len(jobs), func(i int) (string, func(context.Context) ([]T, error)) {
		j := jobs[i]
		return j.src.Label(), func(ctx context.Context) ([]T, error) { return call(ctx, j.src, j.at) }
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
