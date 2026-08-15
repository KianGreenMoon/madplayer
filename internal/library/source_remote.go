package library

import (
	"context"
	"strings"

	"daemonlord.ygg/madplayer/internal/madshare"
)

// remoteSource is somebody else's madshare, read over HTTP.
//
// This is the one thing the HTTP client is for: reaching a machine that is not
// this one (docs/ui/madplayer.md §"Local is a function call"). Nothing here
// re-derives a rule — every row is rendered as it arrived.
type remoteSource struct {
	base  string
	label string
	cl    *madshare.Client
}

func (r remoteSource) ID() string    { return r.base }
func (r remoteSource) Label() string { return r.label }

// client is the HTTP client, or a reason there is none.
//
// A Server always carries one in this program — applyServers builds it — so this
// guards a wiring mistake rather than a supported state. It is here because of
// where the mistake would land: a browse runs on a background goroutine, and a
// nil dereference there takes the whole player down, where an error greys out
// one library and says which (see Problem).
func (r remoteSource) client() (*madshare.Client, error) {
	if r.cl == nil {
		return nil, errNoClient
	}
	return r.cl, nil
}

func (r remoteSource) origin(id int64) Origin {
	return Origin{Source: r.base, Label: r.label, ID: id}
}

func (r remoteSource) Artists(ctx context.Context) ([]*Artist, error) {
	cl, err := r.client()
	if err != nil {
		return nil, err
	}
	rows, err := cl.Artists(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*Artist, 0, len(rows))
	for _, a := range rows {
		out = append(out, &Artist{Name: a.Name, TrackCount: a.TrackCount, Origins: []Origin{r.origin(a.ID)}})
	}
	return out, nil
}

func (r remoteSource) Albums(ctx context.Context, artist Origin) ([]*Album, error) {
	cl, err := r.client()
	if err != nil {
		return nil, err
	}
	rows, err := cl.Albums(ctx, artist.ID)
	if err != nil {
		return nil, err
	}
	out := make([]*Album, 0, len(rows))
	for _, a := range rows {
		al := &Album{
			ArtistName:    a.ArtistName,
			ArtistOrigins: []Origin{r.origin(a.ArtistID)},
			Title:         a.Title,
			TrackCount:    a.TrackCount,
			Origins:       []Origin{r.origin(a.ID)},
		}
		if a.Year != nil {
			al.Year = int(*a.Year)
		}
		out = append(out, al)
	}
	return out, nil
}

func (r remoteSource) AlbumTracks(ctx context.Context, album Origin, albumTitle string) ([]*Track, error) {
	cl, err := r.client()
	if err != nil {
		return nil, err
	}
	rows, err := cl.Tracks(ctx, album.ID)
	if err != nil {
		return nil, err
	}
	out := make([]*Track, 0, len(rows))
	for _, t := range rows {
		out = append(out, r.track(t, albumTitle))
	}
	return out, nil
}

func (r remoteSource) Search(ctx context.Context, q string) (SearchResults, error) {
	var res SearchResults
	cl, err := r.client()
	if err != nil {
		return SearchResults{}, err
	}
	got, err := cl.Search(ctx, q)
	if err != nil {
		return res, err
	}
	for _, a := range got.Artists {
		res.Artists = append(res.Artists, &Artist{
			Name: a.Name, TrackCount: a.TrackCount, Origins: []Origin{r.origin(a.ID)},
		})
	}
	for _, a := range got.Albums {
		al := &Album{
			ArtistName: a.ArtistName, ArtistOrigins: []Origin{r.origin(a.ArtistID)},
			Title: a.Title, TrackCount: a.TrackCount,
			Origins: []Origin{r.origin(a.ID)},
		}
		if a.Year != nil {
			al.Year = int(*a.Year)
		}
		res.Albums = append(res.Albums, al)
	}
	for _, t := range got.Tracks {
		res.Tracks = append(res.Tracks, r.track(t, t.AlbumTitle))
	}
	return res, nil
}

func (r remoteSource) track(t madshare.Track, albumTitle string) *Track {
	out := &Track{
		Title:  t.Title,
		Artist: t.ArtistName,
		Album:  albumTitle,
		Copies: []Copy{{
			Origin: r.origin(t.TagsetID),
			URL:    r.cl.Resolve(t.URL),
			// The hash of the rendition that ACTUALLY plays, taken out of the
			// play URL rather than from the row's `hash` — that one is the
			// ORIGIN blob, which may have been pruned while the recording plays
			// on through a surviving rendition. The wrong one here would cache
			// bytes under a name nothing looks up.
			Hash: hashFromPlayURL(t.URL),
			MIME: t.MimeType,
		}},
	}
	if t.TrackNumber != nil {
		out.TrackNumber = int(*t.TrackNumber)
	}
	if t.DiscNumber != nil {
		d := int(*t.DiscNumber)
		out.DiscNumber = &d
	}
	if t.Duration != nil {
		out.Duration = *t.Duration
	}
	return out
}

// hashFromPlayURL pulls the content hash out of a /files/<hash>/<name> address.
// An address in any other shape yields "", and the caller then keys the cache on
// the URL itself — correct, just not shared between servers.
func hashFromPlayURL(rel string) string {
	rest, ok := strings.CutPrefix(strings.TrimPrefix(rel, "/"), "files/")
	if !ok {
		return ""
	}
	hash, _, ok := strings.Cut(rest, "/")
	if !ok {
		return ""
	}
	return hash
}

// FileName is the last path element of a play address, which is what gives a
// cached blob the extension its decoder is chosen by.
func FileName(u string) string {
	if i := strings.LastIndex(u, "/"); i >= 0 {
		u = u[i+1:]
	}
	if i := strings.IndexAny(u, "?#"); i >= 0 {
		u = u[:i]
	}
	return u
}
