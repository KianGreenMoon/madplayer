package library

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/dhowden/tag"
)

// AcceptedAudio is the extension → MIME allow-list, mirroring the server's
// `acceptedAudioTypes`. The extension is authoritative, not any declared type:
// there is no upload here, only a file on disk, and its name is all we have.
//
// Not every entry can be DECODED yet — see player.Decodable. The list stays the
// server's on purpose, so a file that madshare would accept is one this client
// indexes and can at least show, rather than one it silently pretends is absent.
var AcceptedAudio = map[string]string{
	".mp3":  "audio/mpeg",
	".ogg":  "audio/ogg",
	".oga":  "audio/ogg",
	".flac": "audio/flac",
	".wav":  "audio/wav",
	".mp4":  "audio/mp4",
	".m4a":  "audio/mp4",
	".aac":  "audio/aac",
	".opus": "audio/opus",
}

// ScanSummary reports what a scan did. Failures are counted AND named: a scan
// that quietly indexed 9 of 10 files is a scan that lost one.
type ScanSummary struct {
	Scanned   int
	Added     int
	Updated   int
	Unchanged int
	Failed    int
	Errors    []string
	Elapsed   time.Duration
}

// ScanProgress is called during a scan with the running counts, so a long walk
// over a cold disk can say something rather than appear hung.
type ScanProgress func(ScanSummary)

// Scan walks roots and returns every accepted audio file it finds.
//
// **It only ever reads.** Nothing here creates, moves, renames or writes to the
// scanned tree — the same hard invariant the server's import-in-place sources
// carry (docs/architecture/data-sources.md). A music folder is somebody's
// property, not this program's storage.
//
// prev is the previous scan's tracks keyed by path; an entry whose size and
// mtime are unchanged is carried over WITHOUT re-reading its tags, which is what
// makes a rescan of a large library fast. Pass nil for a cold scan.
func Scan(ctx context.Context, roots []string, prev map[string]*Track, progress ScanProgress) ([]*Track, ScanSummary) {
	started := time.Now()
	var sum ScanSummary
	out := make([]*Track, 0, len(prev))
	seen := make(map[string]struct{}, len(prev))

	for _, root := range roots {
		abs, err := filepath.Abs(root)
		if err != nil {
			sum.Failed++
			sum.Errors = append(sum.Errors, fmt.Sprintf("%s: %v", root, err))
			continue
		}

		walkErr := filepath.WalkDir(abs, func(path string, d fs.DirEntry, err error) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err != nil {
				// An unreadable entry — permissions, a vanished file — is
				// recorded and skipped. One bad subtree must not abort the walk.
				sum.Failed++
				sum.Errors = append(sum.Errors, fmt.Sprintf("%s: %v", path, err))
				return nil
			}
			if d.IsDir() {
				return nil
			}
			mime, ok := AcceptedAudio[strings.ToLower(filepath.Ext(d.Name()))]
			if !ok {
				return nil // not audio; not an error, not counted
			}

			sum.Scanned++
			if _, dup := seen[path]; dup {
				return nil // overlapping roots: index a file once
			}
			seen[path] = struct{}{}

			info, err := d.Info()
			if err != nil {
				sum.Failed++
				sum.Errors = append(sum.Errors, fmt.Sprintf("%s: %v", path, err))
				return nil
			}

			if old, ok := prev[path]; ok && old.Size == info.Size() && old.ModTime.Equal(info.ModTime()) {
				cp := *old
				out = append(out, &cp)
				sum.Unchanged++
			} else {
				t := readTrack(path, mime, info)
				// Carry a duration already measured for an unchanged-looking
				// file so a rescan does not throw away the analysis pass.
				if old != nil && old.Duration > 0 && old.Size == info.Size() {
					t.Duration = old.Duration
				}
				out = append(out, t)
				if ok {
					sum.Updated++
				} else {
					sum.Added++
				}
			}

			if progress != nil && sum.Scanned%64 == 0 {
				progress(sum)
			}
			return nil
		})
		if walkErr != nil && ctx.Err() == nil {
			// WalkDir only surfaces an error when the root itself is unreadable,
			// since the callback always returns nil otherwise.
			sum.Failed++
			sum.Errors = append(sum.Errors, fmt.Sprintf("%s: %v", abs, walkErr))
		}
		if ctx.Err() != nil {
			break
		}
	}

	// Stable order so an unchanged library rebuilds an identical index.
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })

	sum.Elapsed = time.Since(started)
	if progress != nil {
		progress(sum)
	}
	return out, sum
}

// readTrack reads one file's tags. A file whose tags cannot be read is still
// indexed — an untagged file is music too, and dropping it would make the
// library quietly smaller than the folder.
func readTrack(path, mime string, info fs.FileInfo) *Track {
	t := &Track{
		Path:    path,
		Size:    info.Size(),
		ModTime: info.ModTime(),
		MIME:    mime,
	}

	f, err := os.Open(path)
	if err != nil {
		return t
	}
	defer f.Close()

	m, err := tag.ReadFrom(f)
	if err != nil {
		return t // no tags, or an unsupported dialect: filename-derived title
	}

	t.Title = m.Title()
	t.Artist = m.Artist()
	t.AlbumArtist = m.AlbumArtist()
	t.Album = m.Album()
	t.Genre = m.Genre()
	t.Year = m.Year()
	t.TrackNumber, _ = m.Track()

	// A disc tag of 0 means "absent", exactly as the server's ingest treats it
	// (`nullInt` is valid only when i != 0). Disc 0 as a real disc can only
	// arrive from a deliberate edit, and the display rules still keep it
	// distinct from untagged.
	if d, _ := m.Disc(); d != 0 {
		t.DiscNumber = &d
	}
	return t
}
