package library

import (
	"context"
	"strings"

	"daemonlord.ygg/madshare/api"
	"daemonlord.ygg/madshare/app"
	"daemonlord.ygg/madshare/federation"
)

// pairedSource is the community's catalogue, browsed through this device's OWN
// node — what pairing buys a full-node player (madshare's
// docs/plans/full-node-mode.md P2b; the mode is docs/design.md §"Node mode").
//
// It is madnetworkSource's sibling with the directory moved in-process: same
// merged catalog, same rules, answered by app.Madnetwork — the very code the
// web UI's /madnetwork runs — instead of a home server's HTTP endpoints. No
// sign-in, no client, no token: the standing behind every answer is the
// friendships on the Paired nodes page.
//
// Bytes still come from holders over the mesh, never through anybody's relay.
// The fetch plan is asked of the own node at PLAY time (remote.Fetcher's
// paired branch → backend.CommunityHolders), for the same freshness reason the
// server-backed rows ask their endpoint.
// PairedNode is the own node's browse surface, in facade vocabulary — the same
// pattern as New taking app.Library. An alias, not a wrapper, so
// backend.Community()'s value passes straight through.
type PairedNode = app.Madnetwork

type pairedSource struct {
	mn PairedNode
}

// NodeSourceBase is the paired source's stand-in for a server base URL: the
// address of "this device's own node". queue items built from these rows carry
// it as their Base, which is how the fetcher knows to ask the own node for the
// holder plan instead of a server (remote.Fetcher.sourceFor).
const NodeSourceBase = "@node"

func pairedID() string { return NodeSourceBase + MadnetworkMark }

func (p pairedSource) ID() string    { return pairedID() }
func (p pairedSource) Label() string { return madnetworkName }

func (p pairedSource) origin(ref string) Origin {
	return Origin{Source: p.ID(), Label: madnetworkName, Ref: ref}
}

func (p pairedSource) Artists(ctx context.Context) ([]*Artist, error) {
	var out []*Artist
	cursor := ""
	for page := 0; page < artistPageCap; page++ {
		artists, next, err := p.mn.Artists(ctx, "", artistPageLimit, cursor)
		if err != nil {
			return nil, err
		}
		for _, a := range artists {
			out = append(out, &Artist{
				Name:       a.Name,
				TrackCount: int(a.Tracks),
				Origins:    []Origin{p.origin(a.Name)},
			})
		}
		if next == "" {
			break
		}
		cursor = next
	}
	return out, nil
}

func (p pairedSource) Albums(ctx context.Context, artist Origin) ([]*Album, error) {
	rows, err := p.mn.AlbumsByArtist(ctx, artist.Ref)
	if err != nil {
		return nil, err
	}
	out := make([]*Album, 0, len(rows))
	for _, a := range rows {
		al := &Album{
			ArtistName:    artist.Ref,
			ArtistOrigins: []Origin{p.origin(artist.Ref)},
			Title:         a.Title,
			TrackCount:    int(a.Tracks),
			Origins:       []Origin{p.origin(artist.Ref)},
		}
		if a.Year != nil {
			al.Year = int(*a.Year)
		}
		// No CoverRef: the facade has no cover relay yet, and a ref nobody can
		// fetch is worse than the placeholder. The device rows the merge folds
		// these into usually bring the art anyway.
		out = append(out, al)
	}
	return out, nil
}

func (p pairedSource) AlbumTracks(ctx context.Context, album Origin, albumTitle string) ([]*Track, error) {
	rows, err := p.mn.TracksByAlbum(ctx, album.Ref, albumTitle)
	if err != nil {
		return nil, err
	}
	out := make([]*Track, 0, len(rows))
	for _, t := range rows {
		if tr := p.track(t, album.Ref, albumTitle); tr != nil {
			out = append(out, tr)
		}
	}
	return out, nil
}

// track renders one catalogue row; same rules as madnetworkSource.track — a
// row with no fetchable rendition is dropped, a blank credit falls back to the
// album artist. The one divergence: a self-held version's /files/ URL is
// meaningless here (there is no HTTP server behind it), so every copy is a
// network copy — and a self-held hash short-circuits locally inside the node's
// own fetch path anyway, when the merge has not already preferred the device
// row for the same track.
func (p pairedSource) track(t *api.MadnetworkTrack, albumArtist, albumTitle string) *Track {
	if t == nil {
		return nil
	}
	rendition, ok := bestRendition(t.Versions)
	if !ok {
		return nil
	}
	credit := t.Artist
	if strings.TrimSpace(credit) == "" {
		credit = albumArtist
	}
	tr := &Track{
		Title:    t.Title,
		Artist:   credit,
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
	tr.Copies = []Copy{{
		Origin:  p.origin(albumArtist),
		Hash:    rendition.Hash,
		Size:    rendition.Size,
		Codec:   strings.ToLower(rendition.Codec),
		Network: true,
	}}
	return tr
}

// bestRendition is the fetchable pick: the first version carrying a rendition
// with a hash. It is madshare.MadnetworkTrack.Best's walk over the api types —
// the server-backed source's rule — so the two views of the same catalog agree
// on what is playable. Taking Versions[0] blindly dropped rows whose fetchable
// version came second, and offered empty-hash copies nothing could fetch.
func bestRendition(versions []api.MadnetworkVersion) (federation.CatalogRendition, bool) {
	for _, v := range versions {
		if len(v.Renditions) > 0 && v.Renditions[0].Hash != "" {
			return v.Renditions[0], true
		}
	}
	return federation.CatalogRendition{}, false
}

func (p pairedSource) Search(ctx context.Context, q string) (SearchResults, error) {
	res, err := p.mn.Search(ctx, q)
	if err != nil || res == nil {
		return SearchResults{}, err
	}
	out := SearchResults{}
	for _, a := range res.Artists {
		out.Artists = append(out.Artists, &Artist{
			Name:       a.Name,
			TrackCount: int(a.Tracks),
			Origins:    []Origin{p.origin(a.Name)},
		})
	}
	// Album hits are dropped for madnetworkSource's reason: the endpoint answers
	// them by title alone and a merged-catalog album needs artist AND title to
	// open.
	for _, t := range res.Tracks {
		tr := &Track{Title: t.Title, Artist: t.ArtistName, Album: t.AlbumTitle}
		if tr.Artist == "" {
			tr.Artist = t.Artist
		}
		if t.Duration != nil {
			tr.Duration = *t.Duration
		}
		// Size and codec travel with the hash: a track hit PLAYS directly, and
		// a network copy without a codec caches a file with no extension — audio
		// the decoders cannot open (remote.cacheKey's trap).
		c := Copy{
			Origin:  p.origin(t.Artist),
			Hash:    t.Hash,
			Size:    t.Size,
			Codec:   strings.ToLower(t.Codec),
			Network: true,
		}
		if !c.Playable() {
			continue
		}
		tr.Copies = []Copy{c}
		out.Tracks = append(out.Tracks, tr)
	}
	return out, nil
}
