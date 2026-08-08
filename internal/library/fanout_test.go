package library

import (
	"context"
	"errors"
	"testing"
)

// fakeSource stands in for one library. It is not the embedded backend and not
// a server: the fan-out is not supposed to be able to tell.
type fakeSource struct {
	id, label string
	artists   []*Artist
	err       error
}

func (f fakeSource) ID() string    { return f.id }
func (f fakeSource) Label() string { return f.label }

func (f fakeSource) Artists(ctx context.Context) ([]*Artist, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.artists, nil
}

func (f fakeSource) Albums(ctx context.Context, artistID int64) ([]*Album, error) {
	if f.err != nil {
		return nil, f.err
	}
	return []*Album{{Title: f.label + " album", Origins: []Origin{{Source: f.id, Label: f.label, ID: artistID}}}}, nil
}

func (f fakeSource) AlbumTracks(ctx context.Context, albumID int64, albumTitle string) ([]*Track, error) {
	return nil, f.err
}

func (f fakeSource) Search(ctx context.Context, q string) (SearchResults, error) {
	if f.err != nil {
		return SearchResults{}, f.err
	}
	return SearchResults{Artists: f.artists}, nil
}

func withSources(sources ...Source) *Library {
	l := &Library{}
	if len(sources) > 0 {
		l.device = sources[0]
		l.remotes = sources[1:]
	}
	return l
}

// One server being down must never blank the music on this device. That is the
// whole argument for the player working with no network at all.
func TestAnUnreachableServerDoesNotBlankTheDeviceLibrary(t *testing.T) {
	down := errors.New("connection refused")
	l := withSources(
		fakeSource{id: DeviceID, label: DeviceLabel, artists: []*Artist{artist("Burial", 9, dev(1))}},
		fakeSource{id: "http://host:3000", label: "host", err: down},
	)

	rows, probs, err := l.Artists(context.Background())
	if err != nil {
		t.Fatalf("Artists returned a hard error with the device library readable: %v", err)
	}
	if len(rows) != 1 || rows[0].Name != "Burial" {
		t.Fatalf("rows = %v, want the device's own", names(rows))
	}
	if len(probs) != 1 || probs[0].Label != "host" || !errors.Is(probs[0].Err, down) {
		t.Fatalf("problems = %v, want the server named", probs)
	}
}

// Nothing readable anywhere is a real error: an empty list would say "you own
// nothing", which is a much worse lie than "that server did not answer".
func TestEveryLibraryFailingIsAnError(t *testing.T) {
	l := withSources(
		fakeSource{id: DeviceID, label: DeviceLabel, err: errors.New("database is locked")},
		fakeSource{id: "http://host:3000", label: "host", err: errors.New("connection refused")},
	)

	rows, probs, err := l.Artists(context.Background())
	if err == nil {
		t.Fatal("want an error when no library could be read")
	}
	if len(rows) != 0 {
		t.Errorf("rows = %v, want none", names(rows))
	}
	if len(probs) != 2 {
		t.Errorf("problems = %v, want one per library", probs)
	}
}

// Drilling asks exactly the libraries the row came from, with the id it has in
// each: ids are per-library, and 41 on one server is not 41 on another.
func TestDrillingAsksEachLibraryWithItsOwnID(t *testing.T) {
	l := withSources(
		fakeSource{id: DeviceID, label: DeviceLabel},
		fakeSource{id: "http://host:3000", label: "host"},
		fakeSource{id: "http://other:3000", label: "other"},
	)
	ar := &Artist{Name: "Burial", Origins: []Origin{dev(1), srv2(3)}}

	albums, probs, err := l.Albums(context.Background(), ar)
	if err != nil || len(probs) != 0 {
		t.Fatalf("Albums: %v / %v", err, probs)
	}
	if len(albums) != 2 {
		t.Fatalf("albums = %d, want one from each library the artist is in", len(albums))
	}
	for _, al := range albums {
		o := al.Origins[0]
		switch o.Source {
		case DeviceID:
			if o.ID != 1 {
				t.Errorf("device asked with id %d, want 1", o.ID)
			}
		case "http://other:3000":
			if o.ID != 3 {
				t.Errorf("other asked with id %d, want 3", o.ID)
			}
		default:
			t.Errorf("a library the artist is not in was asked: %q", o.Source)
		}
	}
}

// Signing out of a server while its rows are on screen is a normal thing to do,
// not a failure.
func TestAnOriginWhoseServerIsGoneIsSkipped(t *testing.T) {
	l := withSources(fakeSource{id: DeviceID, label: DeviceLabel})
	ar := &Artist{Name: "Burial", Origins: []Origin{dev(1), srv(9)}}

	albums, probs, err := l.Albums(context.Background(), ar)
	if err != nil {
		t.Fatalf("Albums: %v", err)
	}
	if len(probs) != 0 {
		t.Errorf("problems = %v, want none — that library is gone, not broken", probs)
	}
	if len(albums) != 1 {
		t.Errorf("albums = %d, want just the device's", len(albums))
	}
}

func TestRemoteReportsWhetherAnyServerIsConfigured(t *testing.T) {
	l := withSources(fakeSource{id: DeviceID, label: DeviceLabel})
	if l.Remote() {
		t.Error("no servers configured, Remote() should be false")
	}
	l.SetServers([]Server{{Base: "http://host:3000", Label: "host"}})
	if !l.Remote() {
		t.Error("a server is configured, Remote() should be true")
	}
	l.SetServers(nil)
	if l.Remote() {
		t.Error("servers removed, Remote() should be false again")
	}
}
