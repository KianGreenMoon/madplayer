package madshare

import "fmt"

// DurationString renders a track length as m:ss (or h:mm:ss). An absent duration
// — ffprobe was not on PATH when the file was ingested, so the tech columns were
// never filled — renders as an em dash rather than as 0:00, which would claim a
// fact the server never asserted.
func (t Track) DurationString() string {
	if t.Duration == nil || *t.Duration <= 0 {
		return "—"
	}
	total := int(*t.Duration + 0.5)
	h, m, s := total/3600, (total%3600)/60, total%60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%d:%02d", m, s)
}

// Disc reports the track's disc number and whether it had one. Untagged, 0 and N
// are DISTINCT discs (docs/architecture/disc-numbering.md) — hence the bool
// rather than a 0 default, which would merge the untagged ones into disc 0.
func (t Track) Disc() (int64, bool) {
	if t.DiscNumber == nil {
		return 0, false
	}
	return *t.DiscNumber, true
}
