package madshare

import (
	"context"
	"net/url"
	"strconv"
)

// Browsing the madnetwork through a server, rather than browsing that server's
// own library.
//
// These are the endpoints behind the web UI's /madnetwork page, and what a
// listener node gets for signing in: docs/design.md §"Federation:
// madplayer is a listener node" says the account buys "that server's library
// plus madnetwork through it". The server has already pulled and merged every
// friend's catalog into one deduplicated drill-down (docs/architecture/
// federation.md §Catalog); this client browses that view and never assembles
// one of its own.
//
// The server is a DIRECTORY here, not a source of bytes. A row names a content
// hash and who holds it; the audio is fetched from the holders over the mesh
// (internal/remote), so browsing somebody else's catalogue through this server
// never asks it to download anything.
//
// Three differences from the ordinary library calls, all of them the server's
// shape and none of them worth hiding:
//
//   - Rows are addressed by NAME, not by id. A merged catalog has no id space —
//     the ids belong to the nodes it was merged from, and are opaque here.
//   - The artist list is cursor-only. There is no bare-array form to fall back
//     to, so a caller that wants the whole list pages for it.
//   - A track carries VERSIONS: the same title on different claimed recordings
//     stays one row that expands into them, ordered most-widely-held first. A
//     player wants one file, so it takes the first version's first rendition —
//     which is what the server already sorted to the front.

// MadnetworkArtist is a row of GET /api/madnetwork/artists.
type MadnetworkArtist struct {
	Name   string `json:"name"`
	Albums int64  `json:"albums"`
	Tracks int64  `json:"tracks"`
}

// MadnetworkArtistPage is one keyset page of that list. NextCursor is opaque —
// pass it back verbatim, never construct one.
type MadnetworkArtistPage struct {
	Artists    []MadnetworkArtist `json:"artists"`
	NextCursor string             `json:"next_cursor"`
}

// MadnetworkAlbum is a row of GET /api/madnetwork/albums?artist=.
type MadnetworkAlbum struct {
	Title  string `json:"title"`
	Tracks int64  `json:"tracks"`
	Year   *int64 `json:"year"`
}

// MadnetworkRendition is one file of one version: the bytes, and what they are.
//
// Hash and Size are the whole reason this client can play network content
// without the server moving any of it — they are exactly what a swarm fetch
// needs (federation.Transfer takes both).
type MadnetworkRendition struct {
	Hash       string  `json:"hash"`
	Size       int64   `json:"size"`
	Codec      string  `json:"codec"`
	Bitrate    int64   `json:"bitrate"`
	SampleRate int64   `json:"sample_rate"`
	Duration   float64 `json:"duration"`
}

// MadnetworkHolder is a node that has the bytes.
//
// It is carried for display — how many nodes have a track, and whether any of
// them is reachable. The fetch does NOT use it: holders are asked for again at
// play time (GET /api/madnetwork/holders/{hash}), because a browse row can be
// minutes old and the endpoint applies the stale-holder window that a fetch
// plan needs (docs/architecture/federation.md §"Availability & node health").
type MadnetworkHolder struct {
	Name      string `json:"name"`
	LastSeen  int64  `json:"last_seen"`
	Self      bool   `json:"self"`
	Reachable bool   `json:"reachable"`
	Key       string `json:"key"`
}

// MadnetworkVersion is one claimed recording of a track, with its files and the
// nodes holding them.
type MadnetworkVersion struct {
	Renditions []MadnetworkRendition `json:"renditions"`
	Holders    []MadnetworkHolder    `json:"holders"`
	// URL is set only when this server holds the version's best rendition in its
	// own library: a direct play address, no relay hop and no mesh needed.
	URL string `json:"url"`
}

// MadnetworkTrack is a row of GET /api/madnetwork/tracks?artist=&album=.
type MadnetworkTrack struct {
	Title    string  `json:"title"`
	Artist   string  `json:"artist"`
	Track    *int64  `json:"track_number"`
	Disc     *int64  `json:"disc_number"`
	Duration float64 `json:"duration"`

	Versions []MadnetworkVersion `json:"versions"`
}

// Best is the rendition a player should fetch: the first version's first
// rendition. Both orders are the server's — versions most-widely-held first,
// renditions by the quality ladder — so "first" here means "what the server
// already decided is best", not a choice made in this client.
func (t MadnetworkTrack) Best() (MadnetworkVersion, MadnetworkRendition, bool) {
	for _, v := range t.Versions {
		if len(v.Renditions) > 0 && v.Renditions[0].Hash != "" {
			return v, v.Renditions[0], true
		}
	}
	return MadnetworkVersion{}, MadnetworkRendition{}, false
}

// MadnetworkSearchTrack is a track hit. It carries the drill address of the
// album it is on (Artist + AlbumTitle), because a hit is a row somewhere.
type MadnetworkSearchTrack struct {
	Title      string   `json:"title"`
	ArtistName string   `json:"artist_name"`
	Artist     string   `json:"artist"`
	AlbumTitle string   `json:"album_title"`
	Duration   *float64 `json:"duration_seconds"`
	Hash       string   `json:"hash"`
	URL        string   `json:"url"`
}

// MadnetworkSearchResults is GET /api/madnetwork/search?q=, in the same three
// sections as the library's.
type MadnetworkSearchResults struct {
	Artists []MadnetworkArtist      `json:"artists"`
	Albums  []MadnetworkAlbum       `json:"albums"`
	Tracks  []MadnetworkSearchTrack `json:"tracks"`
}

// MadnetworkArtists returns one page of the merged artist list. An empty cursor
// starts at the beginning; q filters by substring.
func (c *Client) MadnetworkArtists(ctx context.Context, q, cursor string, limit int) (MadnetworkArtistPage, error) {
	v := url.Values{}
	if limit > 0 {
		v.Set("limit", strconv.Itoa(limit))
	}
	if q != "" {
		v.Set("q", q)
	}
	if cursor != "" {
		v.Set("cursor", cursor)
	}
	var out MadnetworkArtistPage
	err := c.get(ctx, "/api/madnetwork/artists?"+v.Encode(), &out)
	return out, err
}

// MadnetworkAlbums returns one artist's albums in the merged catalog.
func (c *Client) MadnetworkAlbums(ctx context.Context, artist string) ([]MadnetworkAlbum, error) {
	var out struct {
		Albums []MadnetworkAlbum `json:"albums"`
	}
	err := c.get(ctx, "/api/madnetwork/albums?artist="+url.QueryEscape(artist), &out)
	return out.Albums, err
}

// MadnetworkTracks returns one album's tracks, in the server's order.
func (c *Client) MadnetworkTracks(ctx context.Context, artist, album string) ([]MadnetworkTrack, error) {
	v := url.Values{}
	v.Set("artist", artist)
	v.Set("album", album)
	var out struct {
		Tracks []MadnetworkTrack `json:"tracks"`
	}
	err := c.get(ctx, "/api/madnetwork/tracks?"+v.Encode(), &out)
	return out.Tracks, err
}

// MadnetworkSearch searches the merged catalog.
func (c *Client) MadnetworkSearch(ctx context.Context, q string) (MadnetworkSearchResults, error) {
	var out MadnetworkSearchResults
	err := c.get(ctx, "/api/madnetwork/search?q="+url.QueryEscape(q), &out)
	return out, err
}
