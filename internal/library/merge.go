package library

import (
	"sort"
	"strings"

	"daemonlord.ygg/madshare/database"
)

// Merging several libraries into one browse list.
//
// The identity rule is NOT invented here. It is the one the server already uses
// to merge catalogs from many nodes on the /madnetwork page — same text, one row
// — because these are the same problem: rows from different libraries that share
// no id space. Copying it is what keeps this client and that page from
// disagreeing about what "the same album" is:
//
//	artist  lower(name), the Unknown-artist bucket last
//	album   lower(title) inside an artist, the Other bucket last
//	track   disc + track number + lower(title) inside an album
//
// (database/madnetwork.go: artistBucketLast, albumBucketLast, trackIdent.)
//
// Ordering is the one place a client is allowed to sort. Each source arrives
// already ordered by its own server, and N ordered lists do not concatenate into
// an ordered list — so the merged list is re-ordered by the same keys the server
// used, never by a rule of this client's own.
//
// The BUCKET NAMES are imported from the server rather than spelled again here
// (database.DefaultArtistName / DefaultAlbumTitle): a client that hard-codes
// "Unknown artist" sorts its bucket correctly right up until the server renames
// it.

// foldKey is the case-insensitive identity two rows are compared on.
func foldKey(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// isBucket reports whether a name is one of the server's placeholder buckets,
// which sort after everything real.
func isBucket(name, bucket string) bool { return foldKey(name) == foldKey(bucket) }

// mergeArtists folds one list per source into merged rows. Lists are given
// device-first, and the first source to offer a value wins any tie — so a name's
// capitalisation is the one this device already shows.
func mergeArtists(lists [][]*Artist) []*Artist {
	out := []*Artist{}
	at := map[string]*Artist{}
	for _, list := range lists {
		for _, in := range list {
			k := foldKey(in.Name)
			row, seen := at[k]
			if !seen {
				row = &Artist{Name: in.Name}
				at[k] = row
				out = append(out, row)
			}
			row.Origins = append(row.Origins, in.Origins...)
			row.countFrom(in.TrackCount)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if x, y := isBucket(a.Name, database.DefaultArtistName), isBucket(b.Name, database.DefaultArtistName); x != y {
			return y
		}
		return foldKey(a.Name) < foldKey(b.Name)
	})
	return out
}

// mergeAlbums folds one artist's albums across sources.
func mergeAlbums(lists [][]*Album) []*Album {
	out := []*Album{}
	at := map[string]*Album{}
	for _, list := range lists {
		for _, in := range list {
			k := foldKey(in.Title)
			row, seen := at[k]
			if !seen {
				row = &Album{Title: in.Title, ArtistName: in.ArtistName, Year: in.Year}
				at[k] = row
				out = append(out, row)
			}
			if row.ArtistName == "" {
				row.ArtistName = in.ArtistName
			}
			row.ArtistOrigins = append(row.ArtistOrigins, in.ArtistOrigins...)
			// A year nobody tagged is 0; the first source that knows one wins,
			// rather than a later 0 erasing it.
			if row.Year == 0 {
				row.Year = in.Year
			}
			// Same rule for network art: the first source that names a cover
			// wins. The device source never names one — its covers are files,
			// and a local file always outranks this ref in the UI anyway.
			if row.Cover.Zero() {
				row.Cover = in.Cover
			}
			row.Origins = append(row.Origins, in.Origins...)
			row.countFrom(in.TrackCount)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if x, y := isBucket(a.Title, database.DefaultAlbumTitle), isBucket(b.Title, database.DefaultAlbumTitle); x != y {
			return y
		}
		if a.Year != b.Year {
			return a.Year < b.Year // an untagged year (0) sorts first, as it does in SQL
		}
		return foldKey(a.Title) < foldKey(b.Title)
	})
	return out
}

// mergeTracks folds one album's tracks across sources into one row per logical
// track, each carrying every copy of it that exists.
//
// Two rows are the same track when their disc, track number and title agree —
// the server's trackIdent, minus the album, which is already fixed here. A copy
// is never dropped: a row's copies are what playback picks from, and this device
// having the bytes is what makes a track playable with no network at all.
func mergeTracks(lists [][]*Track) []*Track {
	type ident struct {
		disc, track int
		tagged      bool
		title       string
	}
	key := func(t *Track) ident {
		id := ident{track: t.TrackNumber, title: foldKey(t.Title)}
		if t.DiscNumber != nil {
			id.disc, id.tagged = *t.DiscNumber, true
		}
		return id
	}

	out := []*Track{}
	at := map[ident]*Track{}
	for _, list := range lists {
		for _, in := range list {
			k := key(in)
			row, seen := at[k]
			if !seen {
				row = &Track{
					Title:       in.Title,
					Artist:      in.Artist,
					Album:       in.Album,
					TrackNumber: in.TrackNumber,
					DiscNumber:  in.DiscNumber,
					Duration:    in.Duration,
				}
				at[k] = row
				out = append(out, row)
			}
			if row.Artist == "" {
				row.Artist = in.Artist
			}
			if row.Duration == 0 {
				row.Duration = in.Duration
			}
			if row.Cover.Zero() {
				row.Cover = in.Cover // first named wins, as on album rows
			}
			row.Copies = append(row.Copies, in.Copies...)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		// An untagged disc sorts LAST, which is what (disc_number IS NULL) ASC
		// does in the server's query. Untagged, 0 and N stay three distinct
		// discs — see disc.go.
		if x, y := a.DiscNumber == nil, b.DiscNumber == nil; x != y {
			return y
		}
		if a.DiscNumber != nil && b.DiscNumber != nil && *a.DiscNumber != *b.DiscNumber {
			return *a.DiscNumber < *b.DiscNumber
		}
		if a.TrackNumber != b.TrackNumber {
			return a.TrackNumber < b.TrackNumber
		}
		return foldKey(a.Title) < foldKey(b.Title)
	})
	return out
}

// countFrom folds another source's count into a merged row's.
//
// It takes the MAXIMUM, not the sum, and flags the row approximate once a second
// library contributes. Summing would double-count everything held in two places,
// which is precisely what a merged view is full of; the maximum is the one
// statement that is always true, because merging can only fold rows together and
// never invent them. The UI renders an approximate count as "23+".
func (a *Artist) countFrom(n int) {
	if len(a.Origins) > 1 {
		a.Approx = true
	}
	if n > a.TrackCount {
		a.TrackCount = n
	}
}

func (al *Album) countFrom(n int) {
	if len(al.Origins) > 1 {
		al.Approx = true
	}
	if n > al.TrackCount {
		al.TrackCount = n
	}
}
