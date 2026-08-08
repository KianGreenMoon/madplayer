// Package library is the browse view over the embedded madshare library.
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
// local path a decoder can open, and the disc headers (a display rule the server
// has no opinion about — see disc.go).
package library

import (
	"context"
	"database/sql"

	"daemonlord.ygg/madshare/app"
	"daemonlord.ygg/madshare/database"
)

// Library reads the embedded backend. Every call is a query, so callers fetch on
// navigation and render from what they hold — never per frame.
type Library struct{ src app.Library }

// New wraps the backend's browse surface.
func New(src app.Library) *Library { return &Library{src: src} }

// Artist is a row of the artist list: album artists, in the order given.
type Artist struct {
	ID   int64
	Name string
	// TrackCount covers both credits — being on somebody else's record must not
	// cost an artist the tracks that are theirs.
	TrackCount int
}

// Album belongs to exactly one album-artist.
type Album struct {
	ID       int64
	ArtistID int64
	// ArtistName is the album ARTIST — what a breadcrumb should say when an album
	// is opened straight from a search hit, with no artist level walked through.
	ArtistName string
	Title      string
	Year       int
	// TrackCount is the server's hybrid: an owned album's own total, or just this
	// artist's tracks when the album is reached through a guest appearance. It
	// answers "what will I see if I click this" — except that clicking opens the
	// album WHOLE, because an id names the release and not a slice of it.
	TrackCount int
}

// Track is one appearance, which is what a library track is.
//
// Its identity is TagsetID, not a path: the same identity favourites, playlists
// and the quality picker all key on. Path is where the bytes are on this machine
// right now, and is empty when nothing holds them.
type Track struct {
	TagsetID int64

	Title  string // never empty: the server falls back to the filename
	Artist string // the PERFORMER — the track's own credit, not the album artist
	Album  string

	TrackNumber int
	// DiscNumber is nil when untagged. Untagged, 0 and N are three DISTINCT
	// discs, which is why this is a pointer (see disc.go).
	DiscNumber *int

	// Duration in seconds; 0 means "not known". Comes from ffprobe on the
	// backend, so on a host without it the client's own decoder fills it in for
	// display only.
	Duration float64

	MIME      string
	ObjectKey string // "<hash>/<filename>" — the recording's ladder-best rendition

	// Path is the resolved local file, or "" when the bytes are not reachable:
	// an unplugged drive, an ejected card, a folder somebody moved. On a server
	// that is an incident; on a player it is Tuesday, so it is a state to show
	// and not an error to raise.
	Path string
}

// Available reports whether this machine can currently reach the bytes.
func (t *Track) Available() bool { return t.Path != "" }

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

// Artists is the browse list: album artists only, already ordered, with the
// Unknown bucket last. A name that only ever performs on other people's releases
// is a SEARCH HIT and not a row — the rule, and the one way to get it wrong, are
// in docs/ui/artists-and-performers.md.
func (l *Library) Artists(ctx context.Context) ([]*Artist, error) {
	rows, err := l.src.Artists(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*Artist, 0, len(rows))
	for _, r := range rows {
		out = append(out, &Artist{ID: r.ID, Name: r.Name, TrackCount: r.TrackCount})
	}
	return out, nil
}

// Albums returns one artist's albums in EITHER credit role, in the order given.
func (l *Library) Albums(ctx context.Context, artistID int64) ([]*Album, error) {
	rows, err := l.src.AlbumsByArtist(ctx, artistID)
	if err != nil {
		return nil, err
	}
	out := make([]*Album, 0, len(rows))
	for _, r := range rows {
		out = append(out, &Album{
			ID:         r.ID,
			ArtistID:   r.ArtistID,
			ArtistName: r.ArtistName,
			Title:      r.Title,
			Year:       int(r.Year.Int64),
			TrackCount: r.TrackCount,
		})
	}
	return out, nil
}

// AlbumTracks returns an album's tracks in listening order — the WHOLE album,
// whichever artist the caller drilled in from. The album is passed in so its
// title can travel with the rows into the queue.
func (l *Library) AlbumTracks(ctx context.Context, al *Album) ([]*Track, error) {
	if al == nil {
		return nil, nil
	}
	rows, err := l.src.TracksByAlbum(ctx, al.ID)
	if err != nil {
		return nil, err
	}
	out := make([]*Track, 0, len(rows))
	for _, r := range rows {
		t := &Track{
			TagsetID:    r.TagsetID,
			Title:       r.Title,
			Artist:      r.ArtistName,
			Album:       al.Title,
			TrackNumber: int(r.TrackNumber.Int64),
			DiscNumber:  discOf(r.DiscNumber),
			Duration:    r.DurationSeconds.Float64,
			MIME:        r.MimeType,
			ObjectKey:   r.ObjectKey,
		}
		t.Path, _ = l.src.BlobPath(r.ObjectKey)
		out = append(out, t)
	}
	return out, nil
}

// Search matches artists in both credit roles, album titles and track titles.
//
// Matching both roles is what keeps the album-artists-only browse list a display
// rule rather than missing data: a performer with no release of their own is
// unreachable by browsing, and search is the path to them.
func (l *Library) Search(ctx context.Context, q string) (SearchResults, error) {
	var res SearchResults
	got, err := l.src.Search(ctx, q)
	if err != nil || got == nil {
		return res, err
	}
	for _, r := range got.Artists {
		res.Artists = append(res.Artists, &Artist{ID: r.ID, Name: r.Name, TrackCount: r.TrackCount})
	}
	for _, r := range got.Albums {
		res.Albums = append(res.Albums, &Album{
			ID: r.ID, ArtistID: r.ArtistID, ArtistName: r.ArtistName, Title: r.Title,
			Year: int(r.Year.Int64), TrackCount: r.TrackCount,
		})
	}
	for _, r := range got.Tracks {
		t := &Track{
			TagsetID:    r.TagsetID,
			Title:       r.Title,
			Artist:      r.ArtistName,
			Album:       r.AlbumTitle,
			TrackNumber: int(r.TrackNumber.Int64),
			Duration:    r.DurationSeconds.Float64,
			MIME:        r.MimeType,
			ObjectKey:   r.ObjectKey,
		}
		t.Path, _ = l.src.BlobPath(r.ObjectKey)
		res.Tracks = append(res.Tracks, t)
	}
	return res, nil
}

// Renditions lists the other files of a track's recording, best first — the
// quality picker's list, where index 0 is what Auto resolves to. Unused by the
// UI so far; here because the picker is the one place the client is allowed to
// override which file plays.
func (l *Library) Renditions(ctx context.Context, tagsetID int64) ([]database.DuplicateRendition, error) {
	return l.src.Renditions(ctx, tagsetID)
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
