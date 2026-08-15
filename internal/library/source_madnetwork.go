package library

import (
	"context"
	"strings"

	"daemonlord.ygg/madplayer/internal/madshare"
)

// madnetworkSource is the community's catalogue, browsed THROUGH a server this
// device is signed in to.
//
// It is a second source per server, not a replacement for the first. Signing in
// buys two different things — that server's own library, and the madnetwork it
// can see (docs/ui/madplayer.md §"Federation: madplayer is a listener node") —
// and they are separate sources here because they answer separately: one can be
// empty, or forbidden, or slow, without the other.
//
// **The server is a directory, and never a source of bytes.** Every row it
// returns names a content hash and a size; the audio is fetched from whoever
// holds it, over the mesh (internal/remote). Nothing here asks the server to
// download anything on this device's behalf, which is the whole point of
// browsing the network from a player rather than from the server's own web UI.
//
// Why this device cannot do it alone, since the question is worth answering
// once: catalogs are pulled between FRIENDS, and a listener node has no friends
// — it publishes nothing and appears in nobody's list, which is exactly what
// makes it safe to run on a phone. Its standing on the mesh is a capability
// token from its home server, and that buys the right to fetch bytes from
// strangers, not the right to be handed their catalogues. So the merged view
// comes from the node that legitimately has one.
type madnetworkSource struct {
	base  string
	label string
	cl    *madshare.Client
}

// MadnetworkMark distinguishes this source's id from the ordinary library's at
// the same server. Origins are keyed by source id, and two sources on one server
// sharing an id would drill into each other's rows.
//
// It is a suffix rather than a separate field because an Origin's Source is
// already the addressable name of a library, and this is a different library on
// the same host. Trimming it recovers the base URL, which is what a fetch needs
// to ask that server who holds a hash (ui.madnetworkBase).
const MadnetworkMark = "#madnetwork"

func madnetworkID(base string) string { return base + MadnetworkMark }

func (m madnetworkSource) ID() string    { return madnetworkID(m.base) }
func (m madnetworkSource) Label() string { return m.label }

// client is the HTTP client, or a reason there is none — see remoteSource.client
// for why a nil one is answered rather than dereferenced.
func (m madnetworkSource) client() (*madshare.Client, error) {
	if m.cl == nil {
		return nil, errNoClient
	}
	return m.cl, nil
}

// origin addresses a row by NAME, because the merged catalog has no id space —
// see Origin.
func (m madnetworkSource) origin(ref string) Origin {
	return Origin{Source: m.ID(), Label: m.label, Ref: ref}
}

// artistPageLimit is what the server caps a page at anyway
// (madnetworkArtistPageSize). Asking for exactly that is one round trip per 80
// artists rather than per 50.
const artistPageLimit = 80

// artistPageCap bounds how many pages one browse will walk.
//
// The list is fetched whole because everything else in this client browses whole
// lists — rows are fetched on navigation and held, never queried per frame — and
// a cursor that is only ever followed to the end is a loop, not a feature. The
// cap is here so that a community catalogue growing past what a phone can hold
// degrades into a truncated list rather than an unbounded fetch: 40 pages is
// 3200 artists, well past any catalogue this has met, and if it is ever reached
// the answer is real pagination in the browse list rather than a bigger number.
const artistPageCap = 40

func (m madnetworkSource) Artists(ctx context.Context) ([]*Artist, error) {
	cl, err := m.client()
	if err != nil {
		return nil, err
	}
	var out []*Artist
	cursor := ""
	for page := 0; page < artistPageCap; page++ {
		p, err := cl.MadnetworkArtists(ctx, "", cursor, artistPageLimit)
		if err != nil {
			return nil, err
		}
		for _, a := range p.Artists {
			out = append(out, m.artist(a))
		}
		if p.NextCursor == "" {
			break
		}
		cursor = p.NextCursor
	}
	return out, nil
}

func (m madnetworkSource) artist(a madshare.MadnetworkArtist) *Artist {
	return &Artist{
		Name:       a.Name,
		TrackCount: int(a.Tracks),
		Origins:    []Origin{m.origin(a.Name)},
	}
}

func (m madnetworkSource) Albums(ctx context.Context, artist Origin) ([]*Album, error) {
	cl, err := m.client()
	if err != nil {
		return nil, err
	}
	rows, err := cl.MadnetworkAlbums(ctx, artist.Ref)
	if err != nil {
		return nil, err
	}
	out := make([]*Album, 0, len(rows))
	for _, a := range rows {
		out = append(out, m.album(artist.Ref, a))
	}
	return out, nil
}

func (m madnetworkSource) album(artist string, a madshare.MadnetworkAlbum) *Album {
	al := &Album{
		ArtistName:    artist,
		ArtistOrigins: []Origin{m.origin(artist)},
		Title:         a.Title,
		TrackCount:    int(a.Tracks),
		// The album's own origin carries the ALBUM ARTIST's name: with the title,
		// that is what addresses its tracks (GET /tracks?artist=&album=).
		Origins: []Origin{m.origin(artist)},
	}
	if a.Year != nil {
		al.Year = int(*a.Year)
	}
	return al
}

func (m madnetworkSource) AlbumTracks(ctx context.Context, album Origin, albumTitle string) ([]*Track, error) {
	cl, err := m.client()
	if err != nil {
		return nil, err
	}
	rows, err := cl.MadnetworkTracks(ctx, album.Ref, albumTitle)
	if err != nil {
		return nil, err
	}
	out := make([]*Track, 0, len(rows))
	for _, t := range rows {
		if tr := m.track(t, albumTitle); tr != nil {
			out = append(out, tr)
		}
	}
	return out, nil
}

// track renders one catalogue row.
//
// A row with no fetchable rendition is DROPPED rather than shown unplayable.
// That is the opposite of this client's rule for the device library, where a
// missing file is an unplugged drive worth saying out loud — here it means the
// catalogue names audio nobody offered a hash for, which is not a track anybody
// can do anything about.
func (m madnetworkSource) track(t madshare.MadnetworkTrack, albumTitle string) *Track {
	version, rendition, ok := t.Best()
	if !ok {
		return nil
	}
	tr := &Track{
		Title:    t.Title,
		Artist:   t.Artist,
		Album:    albumTitle,
		Duration: t.Duration,
	}
	if t.Track != nil {
		tr.TrackNumber = int(*t.Track)
	}
	if t.Disc != nil {
		d := int(*t.Disc)
		tr.DiscNumber = &d
	}
	if tr.Duration == 0 {
		tr.Duration = rendition.Duration
	}

	c := Copy{
		Origin:  m.origin(t.Artist),
		Hash:    rendition.Hash,
		Size:    rendition.Size,
		Codec:   strings.ToLower(rendition.Codec),
		Network: true,
	}
	// A version this server holds in its OWN library comes with a direct play
	// address, and a direct address is worth taking: it is one HTTP request to a
	// machine already trusted with the credential, against a holder lookup and a
	// swarm for the same bytes. It is not the cache-through relay — that would
	// make the server fetch somebody else's audio and keep it, which is exactly
	// what browsing the network from here is meant to avoid.
	if version.URL != "" {
		c.URL = m.cl.Resolve(version.URL)
		c.Network = false
	}
	tr.Copies = []Copy{c}
	return tr
}

func (m madnetworkSource) Search(ctx context.Context, q string) (SearchResults, error) {
	cl, err := m.client()
	if err != nil {
		return SearchResults{}, err
	}
	res, err := cl.MadnetworkSearch(ctx, q)
	if err != nil {
		return SearchResults{}, err
	}
	out := SearchResults{}
	for _, a := range res.Artists {
		out.Artists = append(out.Artists, m.artist(a))
	}
	// Album hits are DROPPED, and that is a deliberate loss. The endpoint answers
	// them by title alone, and an album on the merged catalog is addressed by
	// artist AND title — so a row built from one could be drawn but not opened,
	// and a row that does nothing when clicked is worse than a row that is not
	// there. The album is still reachable: its artist matches the same query, and
	// its tracks come back as track hits that name it.
	for _, t := range res.Tracks {
		tr := &Track{Title: t.Title, Artist: t.ArtistName, Album: t.AlbumTitle}
		if tr.Artist == "" {
			tr.Artist = t.Artist
		}
		if t.Duration != nil {
			tr.Duration = *t.Duration
		}
		c := Copy{Origin: m.origin(t.Artist), Hash: t.Hash, Network: true}
		if t.URL != "" {
			c.URL = m.cl.Resolve(t.URL)
			c.Network = false
		}
		if !c.Playable() {
			continue
		}
		tr.Copies = []Copy{c}
		out.Tracks = append(out.Tracks, tr)
	}
	return out, nil
}
