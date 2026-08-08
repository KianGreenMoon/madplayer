package library

import "strconv"

// Disc numbering (docs/architecture/disc-numbering.md).
//
// Untagged, 0 and N are THREE DISTINCT discs and are never folded together —
// which is why a disc number is a *int here and not an int.
//
// This is the one library rule that stays on the client, because it is a DISPLAY
// rule: the server orders an album by COALESCE(disc_number, 1), track_number,
// title, so same-disc tracks arrive contiguous, and turning that into "Disc N"
// separators is the client's job. Ordering is not — a client that re-sorts what
// arrived would list an album differently from the same album on a server.

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
	return "Disc " + strconv.Itoa(k.Number)
}

// IsMultiDisc reports whether an album spans more than one distinct disc — the
// gate for showing separators at all. Headers are VISUAL ONLY: the queue stays
// one flat ordered list, so a header must never shift a track index.
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

// DurationString renders m:ss (or h:mm:ss); an unknown length is an em dash, not
// 0:00, which would claim a fact nothing has measured.
func (t *Track) DurationString() string {
	if t.Duration <= 0 {
		return "—"
	}
	total := int(t.Duration + 0.5)
	h, m, s := total/3600, (total%3600)/60, total%60
	if h > 0 {
		return strconv.Itoa(h) + ":" + pad2(m) + ":" + pad2(s)
	}
	return strconv.Itoa(m) + ":" + pad2(s)
}

func pad2(n int) string {
	if n < 10 {
		return "0" + strconv.Itoa(n)
	}
	return strconv.Itoa(n)
}
