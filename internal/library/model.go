package library

import "time"

// Track is one audio file on this device.
//
// Its identity is Path. The server identifies a track by content hash and by
// appearance id; a local player does not, deliberately — hashing a 50 GB library
// on every scan buys deduplication nobody asked for, and the path is what the
// decoder needs anyway. The consequence is honest and worth stating: the same
// audio in two folders is two tracks here.
type Track struct {
	Path    string    `json:"path"` // absolute; the identity
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mod_time"`
	MIME    string    `json:"mime"`

	// Raw tag text, kept as read. Never silently rewritten — the entity layer is
	// an overlay on top of these, the same way the server treats them.
	Title       string `json:"title"`
	Artist      string `json:"artist"`       // the performer
	AlbumArtist string `json:"album_artist"` // the release's artist
	Album       string `json:"album"`
	Genre       string `json:"genre,omitempty"`
	Year        int    `json:"year,omitempty"`
	TrackNumber int    `json:"track_number,omitempty"`

	// DiscNumber is nil when untagged. Untagged, 0 and N are three DISTINCT
	// discs — a plain int would fold the first two together, which is the bug
	// docs/architecture/disc-numbering.md exists to prevent.
	DiscNumber *int `json:"disc_number,omitempty"`

	// Duration in seconds; 0 means "not known yet". Filled in by a background
	// pass after the scan, because opening every file with a decoder is far
	// slower than reading tags and must not hold up the first render. A row with
	// no duration shows "—" rather than a wrong 0:00.
	Duration float64 `json:"duration,omitempty"`

	// Resolved entity ids, assigned by the index. Not persisted: they are
	// rebuilt from the tags on load, so a change to the identity rules takes
	// effect without a rescan.
	AlbumArtistID int64 `json:"-"`
	ArtistID      int64 `json:"-"`
	AlbumID       int64 `json:"-"`
}

// DisplayTitle is the row's text: the title tag, or the filename.
func (t *Track) DisplayTitle() string { return EffectiveTitle(t.Title, t.Path) }

// DurationString renders m:ss (or h:mm:ss); an unknown length is an em dash, not
// 0:00, which would claim a fact nothing has measured.
func (t *Track) DurationString() string {
	if t.Duration <= 0 {
		return "—"
	}
	total := int(t.Duration + 0.5)
	h, m, s := total/3600, (total%3600)/60, total%60
	if h > 0 {
		return itoa(h) + ":" + pad2(m) + ":" + pad2(s)
	}
	return itoa(m) + ":" + pad2(s)
}

func pad2(n int) string {
	if n < 10 {
		return "0" + itoa(n)
	}
	return itoa(n)
}

// Artist is an album-artist or a performer — one entity type serving both roles,
// exactly as the server models it, so a name that plays both parts is one row.
type Artist struct {
	ID       int64
	Name     string
	NormName string

	// TrackCount counts every track this artist is credited on, in EITHER role.
	// Being on somebody else's record must not cost an artist the tracks that
	// are theirs.
	TrackCount int
}

// Album belongs to exactly one album-artist, which is why it needs no
// album_artist_id — an album has one artist by definition.
type Album struct {
	ID        int64
	ArtistID  int64
	Title     string
	NormTitle string
	Year      int

	// TrackCount is the album's own total when listed under its album-artist,
	// but only the performer's tracks when the album reached the list because
	// they guest on it. That hybrid is the point: the count answers "what will I
	// see if I click this".
	TrackCount int
}

// IsUnknownArtist reports whether this is the Unknown bucket, which sorts last
// in every list — it is the one row nobody is looking for.
func (a *Artist) IsUnknownArtist() bool { return a.NormName == NormalizeKey(DefaultArtistName) }

// IsOtherAlbum reports whether this is the "Other" bucket, which sorts last
// among an artist's albums for the same reason.
func (a *Album) IsOtherAlbum() bool { return a.NormTitle == NormalizeKey(DefaultAlbumTitle) }
