package library

import (
	"sort"
	"strings"
)

// SearchLimit caps each section of a search result, matching the server's cap.
const SearchLimit = 50

// Index is the browsable library: entities resolved from the scanned tracks,
// plus the three browse queries and search.
//
// It is rebuilt from tracks rather than persisted, so a change to the identity
// rules takes effect on the next start without a rescan.
type Index struct {
	tracks  []*Track
	artists []*Artist
	albums  []*Album

	byID      map[int64]*Artist
	albumByID map[int64]*Album

	tracksByAlbum map[int64][]*Track

	// artistList is the browse list: ALBUM ARTISTS only, already sorted.
	artistList []*Artist
}

type albumKey struct {
	artistID  int64
	normTitle string
}

// Build resolves entities over the scanned tracks and returns the index.
//
// The resolution is the server's, ported: an album-artist and a performer per
// track, one artists table serving both roles, albums identified by
// (album-artist, normalized title). See identity.go for why it is a port.
func Build(tracks []*Track) *Index {
	ix := &Index{
		tracks:        tracks,
		byID:          make(map[int64]*Artist),
		albumByID:     make(map[int64]*Album),
		tracksByAlbum: make(map[int64][]*Track),
	}

	artistByNorm := make(map[string]*Artist)
	albumByKey := make(map[albumKey]*Album)
	var nextArtistID, nextAlbumID int64

	resolveArtist := func(display string) *Artist {
		norm := NormalizeKey(display)
		if a, ok := artistByNorm[norm]; ok {
			return a
		}
		nextArtistID++
		a := &Artist{ID: nextArtistID, Name: display, NormName: norm}
		artistByNorm[norm] = a
		ix.artists = append(ix.artists, a)
		ix.byID[a.ID] = a
		return a
	}

	for _, t := range tracks {
		albumArtist := resolveArtist(EffectiveArtist(t.AlbumArtist, t.Artist))
		performer := resolveArtist(EffectiveTrackArtist(t.Artist, t.AlbumArtist))
		t.AlbumArtistID = albumArtist.ID
		t.ArtistID = performer.ID

		title := EffectiveAlbumTitle(t.Album)
		key := albumKey{artistID: albumArtist.ID, normTitle: NormalizeKey(title)}
		al, ok := albumByKey[key]
		if !ok {
			nextAlbumID++
			al = &Album{ID: nextAlbumID, ArtistID: albumArtist.ID, Title: title, NormTitle: key.normTitle}
			albumByKey[key] = al
			ix.albums = append(ix.albums, al)
			ix.albumByID[al.ID] = al
		}
		// The album's year comes from the first track that supplies one, and is
		// not recomputed afterwards — otherwise one mistagged track rewrites the
		// album every rescan.
		if al.Year == 0 && t.Year != 0 {
			al.Year = t.Year
		}
		al.TrackCount++
		t.AlbumID = al.ID
		ix.tracksByAlbum[al.ID] = append(ix.tracksByAlbum[al.ID], t)

		// An artist's track count covers BOTH roles, counted once when the two
		// resolve to the same entity — the normal single-artist release.
		albumArtist.TrackCount++
		if performer.ID != albumArtist.ID {
			performer.TrackCount++
		}
	}

	// The browse list is album artists only. A name that only ever appears as a
	// performer on other people's releases is a SEARCH HIT, not a row — see
	// docs/ui/artists-and-performers.md §"The one way to get this wrong", which
	// is precisely the mistake of building this list from track rows.
	isAlbumArtist := make(map[int64]bool, len(ix.artists))
	for _, t := range tracks {
		isAlbumArtist[t.AlbumArtistID] = true
	}
	for _, a := range ix.artists {
		if isAlbumArtist[a.ID] {
			ix.artistList = append(ix.artistList, a)
		}
	}
	sortArtists(ix.artistList)

	for id := range ix.tracksByAlbum {
		sortTracks(ix.tracksByAlbum[id])
	}
	return ix
}

// sortArtists orders by name, with the Unknown bucket pinned last — it is the
// one row nobody is looking for.
func sortArtists(list []*Artist) {
	sort.SliceStable(list, func(i, j int) bool {
		ui, uj := list[i].IsUnknownArtist(), list[j].IsUnknownArtist()
		if ui != uj {
			return uj
		}
		return strings.ToLower(list[i].Name) < strings.ToLower(list[j].Name)
	})
}

// sortTracks orders an album: disc, then track number, then title — the
// server's `COALESCE(disc_number, 1), track_number, title`. An untagged track
// number sorts before numbered ones, which keeps them together rather than
// scattering them through the list.
func sortTracks(list []*Track) {
	sort.SliceStable(list, func(i, j int) bool {
		a, b := list[i], list[j]
		if da, db := DiscOrder(a.DiscNumber), DiscOrder(b.DiscNumber); da != db {
			return da < db
		}
		if a.TrackNumber != b.TrackNumber {
			return a.TrackNumber < b.TrackNumber
		}
		return strings.ToLower(a.DisplayTitle()) < strings.ToLower(b.DisplayTitle())
	})
}

// Tracks returns every indexed track, in scan order.
func (ix *Index) Tracks() []*Track { return ix.tracks }

// Artists is the browse list: album artists, Unknown last.
func (ix *Index) Artists() []*Artist { return ix.artistList }

// Artist looks one up by id.
func (ix *Index) Artist(id int64) *Artist { return ix.byID[id] }

// Album looks one up by id.
func (ix *Index) Album(id int64) *Album { return ix.albumByID[id] }

// Albums returns an artist's albums: the ones they own as album-artist, UNION
// the ones they merely perform on. Being on somebody else's record must not
// cost an artist the tracks that are theirs.
//
// The returned TrackCount is the hybrid the server sends: an owned album counts
// all its tracks, a guest appearance counts only this artist's. The count
// answers "what will I see if I click this" — except that clicking always opens
// the album WHOLE, because an album is addressed by id and an id names the
// release, not a slice of it. The header naming the album artist is the
// explanation.
func (ix *Index) Albums(artistID int64) []*Album {
	var out []*Album
	for _, al := range ix.albums {
		owned := al.ArtistID == artistID
		count := 0
		for _, t := range ix.tracksByAlbum[al.ID] {
			if owned || t.ArtistID == artistID {
				count++
			}
		}
		if count == 0 {
			continue
		}
		cp := *al
		cp.TrackCount = count
		out = append(out, &cp)
	}

	// Named albums by year then title; the "Other" bucket last.
	sort.SliceStable(out, func(i, j int) bool {
		oi, oj := out[i].IsOtherAlbum(), out[j].IsOtherAlbum()
		if oi != oj {
			return oj
		}
		if out[i].Year != out[j].Year {
			return out[i].Year < out[j].Year
		}
		return strings.ToLower(out[i].Title) < strings.ToLower(out[j].Title)
	})
	return out
}

// AlbumTracks returns an album's tracks in listening order — the WHOLE album,
// regardless of which artist the caller drilled in from.
func (ix *Index) AlbumTracks(albumID int64) []*Track {
	return ix.tracksByAlbum[albumID]
}

// SearchResults mirrors the server's three-section shape.
type SearchResults struct {
	Artists []*Artist
	Albums  []*Album
	Tracks  []*Track
}

// Search matches a case-insensitive substring against artist names in BOTH
// roles, album titles and track titles.
//
// Matching both roles is what keeps the album-artists-only browse list a display
// rule rather than missing data: a performer with no release of their own is
// unreachable by browsing, and search is the path to them. A client that ships
// browse without search has turned rule 1 into a hole in the library.
func (ix *Index) Search(q string) SearchResults {
	needle := NormalizeKey(q)
	var res SearchResults
	if needle == "" {
		return res
	}

	// Artists: either role. Credited-anywhere is the test, so a pure performer
	// is a hit even though they are not a browse row.
	for _, a := range ix.artists {
		if len(res.Artists) >= SearchLimit {
			break
		}
		if strings.Contains(a.NormName, needle) {
			res.Artists = append(res.Artists, a)
		}
	}
	sortArtists(res.Artists)

	for _, al := range ix.albums {
		if len(res.Albums) >= SearchLimit {
			break
		}
		if strings.Contains(al.NormTitle, needle) {
			res.Albums = append(res.Albums, al)
		}
	}

	// A matching album deliberately does NOT spill its tracks in here: those
	// rows duplicate an album row already on screen. Performer matches are the
	// opposite — they add rows reachable no other way.
	for _, t := range ix.tracks {
		if len(res.Tracks) >= SearchLimit {
			break
		}
		if strings.Contains(NormalizeKey(t.DisplayTitle()), needle) {
			res.Tracks = append(res.Tracks, t)
		}
	}
	return res
}

// ArtistName resolves a track's performer for display. Inside a track list the
// per-row artist is the PERFORMER — the track's own credit — while the header
// names the album artist. That is what makes a compilation readable.
func (ix *Index) ArtistName(t *Track) string {
	if a := ix.byID[t.ArtistID]; a != nil {
		return a.Name
	}
	return DefaultArtistName
}
