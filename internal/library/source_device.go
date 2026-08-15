package library

import (
	"context"
	"strings"

	"daemonlord.ygg/madshare/app"
)

// deviceSource is the library on this machine, read through the embedded
// backend's facade — direct Go calls, no listener and no port
// (docs/ui/madplayer.md §"Local is a function call").
type deviceSource struct{ lib app.Library }

// DeviceLabel is what this machine's library is called on screen.
const DeviceLabel = "This device"

func (d deviceSource) ID() string    { return DeviceID }
func (d deviceSource) Label() string { return DeviceLabel }

func (d deviceSource) origin(id int64) Origin {
	return Origin{Source: DeviceID, Label: DeviceLabel, ID: id}
}

func (d deviceSource) Artists(ctx context.Context) ([]*Artist, error) {
	rows, err := d.lib.Artists(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*Artist, 0, len(rows))
	for _, r := range rows {
		out = append(out, &Artist{
			Name:       r.Name,
			TrackCount: r.TrackCount,
			Origins:    []Origin{d.origin(r.ID)},
		})
	}
	return out, nil
}

func (d deviceSource) Albums(ctx context.Context, artist Origin) ([]*Album, error) {
	artistID := artist.ID
	rows, err := d.lib.AlbumsByArtist(ctx, artistID)
	if err != nil {
		return nil, err
	}
	out := make([]*Album, 0, len(rows))
	for _, r := range rows {
		out = append(out, &Album{
			ArtistName:    r.ArtistName,
			ArtistOrigins: []Origin{d.origin(r.ArtistID)},
			Title:         r.Title,
			Year:          int(r.Year.Int64),
			// The server's hybrid count: an owned album's own total, or just
			// this artist's tracks when the album is reached through a guest
			// appearance. It answers "what will I see if I click this" — except
			// that clicking opens the album WHOLE, because an id names the
			// release and not a slice of it.
			TrackCount: r.TrackCount,
			Origins:    []Origin{d.origin(r.ID)},
		})
	}
	return out, nil
}

func (d deviceSource) AlbumTracks(ctx context.Context, album Origin, albumTitle string) ([]*Track, error) {
	albumID := album.ID
	rows, err := d.lib.TracksByAlbum(ctx, albumID)
	if err != nil {
		return nil, err
	}
	out := make([]*Track, 0, len(rows))
	for _, r := range rows {
		t := &Track{
			Title:       r.Title,
			Artist:      r.ArtistName,
			Album:       albumTitle,
			TrackNumber: int(r.TrackNumber.Int64),
			DiscNumber:  discOf(r.DiscNumber),
			Duration:    r.DurationSeconds.Float64,
			Copies:      []Copy{d.copy(r.TagsetID, r.ObjectKey, r.MimeType)},
		}
		out = append(out, t)
	}
	return out, nil
}

func (d deviceSource) Search(ctx context.Context, q string) (SearchResults, error) {
	var res SearchResults
	got, err := d.lib.Search(ctx, q)
	if err != nil || got == nil {
		return res, err
	}
	for _, r := range got.Artists {
		res.Artists = append(res.Artists, &Artist{
			Name: r.Name, TrackCount: r.TrackCount, Origins: []Origin{d.origin(r.ID)},
		})
	}
	for _, r := range got.Albums {
		res.Albums = append(res.Albums, &Album{
			ArtistName: r.ArtistName, ArtistOrigins: []Origin{d.origin(r.ArtistID)},
			Title: r.Title, Year: int(r.Year.Int64),
			TrackCount: r.TrackCount, Origins: []Origin{d.origin(r.ID)},
		})
	}
	for _, r := range got.Tracks {
		res.Tracks = append(res.Tracks, &Track{
			Title:       r.Title,
			Artist:      r.ArtistName,
			Album:       r.AlbumTitle,
			TrackNumber: int(r.TrackNumber.Int64),
			Duration:    r.DurationSeconds.Float64,
			Copies:      []Copy{d.copy(r.TagsetID, r.ObjectKey, r.MimeType)},
		})
	}
	return res, nil
}

// copy resolves a row's object key to bytes on this machine.
//
// A false return from BlobPath is the normal "that drive isn't connected" case,
// not a missing track, so the copy is still recorded — with no path. The UI says
// which, and the merge still lets a server's copy of the same track play.
func (d deviceSource) copy(tagsetID int64, objectKey, mime string) Copy {
	path, _ := d.lib.BlobPath(objectKey)
	hash, _, _ := strings.Cut(objectKey, "/")
	return Copy{
		Origin: d.origin(tagsetID),
		Path:   path,
		Hash:   hash,
		MIME:   mime,
	}
}
