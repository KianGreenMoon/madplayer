package library

import (
	"path/filepath"
	"strings"

	"golang.org/x/text/unicode/norm"
)

// The identity rules below are ports of the server's
// (`database/entities.go`, specified in docs/architecture/artist-album-model.md
// §"Identity rules"). They are duplicated rather than imported because madplayer
// is a separate module that must run with no server at all — but they are a PORT,
// not a reinvention: the same library scanned here and uploaded there has to
// produce the same artists, the same albums and the same buckets, or the offline
// and connected halves of this client disagree about what the user owns.
//
// Pinned by identity_test.go against the worked examples in that doc.

// The two placeholder buckets. Both are real names, folded into the dedup keys
// rather than left as display-layer fallbacks, so a file literally tagged
// "Unknown artist" lands IN the bucket instead of beside it.
const (
	DefaultArtistName = "Unknown artist"
	DefaultAlbumTitle = "Other"
)

// NormalizeKey is the dedup key for an artist or album display string:
// Unicode NFC → trim → collapse internal whitespace → lowercase. No
// "the "-stripping and no fuzzy folding — predictability over recall, because a
// wrong merge is worse than a missed one.
func NormalizeKey(s string) string {
	s = norm.NFC.String(s)
	// Fields splits on Unicode whitespace and drops empties, so Join trims the
	// ends and collapses internal runs in one step.
	s = strings.Join(strings.Fields(s), " ")
	return strings.ToLower(s)
}

// EffectiveArtist is the ALBUM-level artist, the one that browse-by-artist
// groups on: album_artist, then artist, else the Unknown bucket.
func EffectiveArtist(albumArtist, artist string) string {
	return firstNamed(albumArtist, artist)
}

// EffectiveTrackArtist is the track's PERFORMER: artist, then album_artist, else
// the Unknown bucket. The precedence is deliberately the reverse of
// EffectiveArtist — the track's own credit wins — which is what makes a
// compilation readable and is the whole reason the two roles exist.
func EffectiveTrackArtist(artist, albumArtist string) string {
	return firstNamed(artist, albumArtist)
}

func firstNamed(candidates ...string) string {
	for _, s := range candidates {
		if NormalizeKey(s) != "" {
			return strings.TrimSpace(norm.NFC.String(s))
		}
	}
	return DefaultArtistName
}

// EffectiveAlbumTitle is the album's display title, or the "Other" bucket when
// the tag is empty. Every artist gets their own "Other" — the bucket is keyed
// per artist, so two artists' untagged tracks never merge into one album.
func EffectiveAlbumTitle(album string) string {
	if NormalizeKey(album) == "" {
		return DefaultAlbumTitle
	}
	return strings.TrimSpace(norm.NFC.String(album))
}

// EffectiveTitle is the track's display title, falling back to the filename with
// its extension stripped. A title is never empty: a row with no text at all is
// unclickable and unsearchable, which is worse than a filename.
func EffectiveTitle(title, path string) string {
	if NormalizeKey(title) != "" {
		return strings.TrimSpace(norm.NFC.String(title))
	}
	base := filepath.Base(path)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

// --- Disc numbering (docs/architecture/disc-numbering.md) --------------------
//
// Untagged, 0 and N are THREE DISTINCT discs and are never folded together. The
// scanner maps a tag of 0 to untagged, exactly as the server's ingest does
// (`nullInt` sets Valid only when i != 0), so disc 0 can only arrive from a
// deliberate edit — but the display rules still keep it separate from untagged.

// DiscKey is a disc's identity. Two tracks share a disc iff their keys are
// equal, which is why Tagged is a field rather than 0 standing in for "absent".
type DiscKey struct {
	Number int
	Tagged bool
}

// KeyOfDisc builds the grouping key.
func KeyOfDisc(disc *int) DiscKey {
	if disc == nil {
		return DiscKey{}
	}
	return DiscKey{Number: *disc, Tagged: true}
}

// Label is the "Disc N" heading; an untagged disc renders "Disc —".
func (k DiscKey) Label() string {
	if !k.Tagged {
		return "Disc —"
	}
	return "Disc " + itoa(k.Number)
}

// DiscOrder is the ORDER BY value, and it is deliberately NOT the same as the
// grouping key: the server sorts an album by `COALESCE(disc_number, 1)`, so an
// untagged disc sorts where disc 1 does rather than last. Ordering a local album
// any other way would list it differently from the same album on a server, and
// the cross-client rule is that the client renders the server's order rather
// than inventing one. Same-disc tracks still come out contiguous, which is all
// the "Disc N" headers need.
func DiscOrder(disc *int) int {
	if disc == nil {
		return 1
	}
	return *disc
}

// IsMultiDisc reports whether an album spans more than one distinct disc — the
// gate for showing "Disc N" separators at all. Headers are VISUAL ONLY: the
// queue stays one flat ordered list, so a header must never shift a track index.
func IsMultiDisc(tracks []*Track) bool {
	seen := make(map[DiscKey]struct{}, 2)
	for _, t := range tracks {
		seen[KeyOfDisc(t.DiscNumber)] = struct{}{}
		if len(seen) > 1 {
			return true
		}
	}
	return false
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
