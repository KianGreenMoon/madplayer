package player

// Seeking a still-arriving track by fetching the SEEKED region first.
//
// This is what the web UI gets from the browser for free: a scrub becomes a
// Range request at an estimated byte offset, the server delivers from there
// (the madnetwork relay even tells its swarm to fetch that chunk first), and
// the browser's decoder resynchronizes mid-stream. The native spelling of the
// same thing lives here.
//
// Two formats know how to start mid-stream, and they are the two that matter:
//
//   - mp3: go-mp3's frame reader slides byte-by-byte until it finds a valid
//     sync header, so a stream cut at an arbitrary offset simply works. The
//     position is the byte-fraction ESTIMATE — exactly a browser's accuracy.
//   - flac: frames are self-contained, sync-coded, CRC-8-verified, and their
//     headers carry the EXACT sample number — so after our own resync scan the
//     landing position is sample-accurate. The metadata header the parser
//     insists on is fetched separately (it is small) and spliced in front.
//     The decoder's own Seek is NOT the answer: without an embedded seek
//     table, mewkiz builds one by parsing every frame of the whole file —
//     the very download this exists to avoid.
//
// Everything else (ogg, wav) falls back to decode-and-discard over the
// sequential fill, which is where every error here lands too: a failed range
// seek costs the fast path, never the seek.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/mewkiz/flac/frame"

	"daemonlord.ygg/madplayer/internal/queue"
)

// RangeFetcher is the optional half of Fetcher: byte-addressed access to a
// remote track. Asserted rather than required, so a fetcher without it (tests,
// an offline build) degrades to the decode-skip path instead of failing.
type RangeFetcher interface {
	// TrackSize is the track's total byte size — what turns seconds into an
	// offset.
	TrackSize(ctx context.Context, item *queue.Item) (int64, error)
	// OpenRange reads the track from a byte offset onward.
	OpenRange(ctx context.Context, item *queue.Item, offset int64) (io.ReadCloser, error)
}

// openRanged opens item's audio mid-stream, standing at (approximately)
// seconds. cur is the already-open sequential source, consulted for the sample
// rate and, when it knows one, the length. The caller keeps cur alive on
// success (the background fill's waiter) and falls back to it on error.
func (p *Player) openRanged(ctx context.Context, item *queue.Item, seconds float64, cur *source) (*source, error) {
	p.mu.Lock()
	fetch := p.fetch
	p.mu.Unlock()
	rf, ok := fetch.(RangeFetcher)
	if !ok {
		return nil, errors.New("the fetcher serves no byte ranges")
	}
	ext := strings.ToLower(filepath.Ext(item.URL))
	if ext != ".mp3" && ext != ".flac" {
		return nil, fmt.Errorf("no mid-stream start for %s", ext)
	}

	rate := float64(cur.format.SampleRate)
	if rate <= 0 {
		return nil, errors.New("the source reports no sample rate")
	}
	totalSec := 0.0
	if n := cur.streamer.Len(); n > 0 {
		totalSec = float64(n) / rate
	}
	if totalSec <= 0 {
		totalSec = item.Duration
	}
	if totalSec <= 0 {
		return nil, errors.New("the track's length is unknown")
	}

	size, err := rf.TrackSize(ctx, item)
	if err != nil {
		return nil, err
	}
	frac := seconds / totalSec
	if frac < 0 {
		frac = 0
	}
	// Never aim at the very last bytes: a stream opened at EOF decodes nothing
	// and ends the track, turning a scrub near the edge into a skip.
	if frac > 0.99 {
		frac = 0.99
	}
	off := int64(frac * float64(size))

	rc, err := rf.OpenRange(ctx, item, off)
	if err != nil {
		return nil, err
	}

	switch ext {
	case ".mp3":
		// go-mp3 finds the next frame sync on its own; the response body is not
		// an io.Seeker, so the decoder takes its streaming path.
		src, err := openReader(rc, ext)
		if err != nil {
			rc.Close()
			return nil, err
		}
		src.base = int(seconds*rate) - src.streamer.Position()
		return src, nil
	default: // ".flac"
		hr, err := rf.OpenRange(ctx, item, 0)
		if err != nil {
			rc.Close()
			return nil, err
		}
		header, herr := readFLACHeader(hr)
		hr.Close()
		if herr != nil {
			rc.Close()
			return nil, herr
		}
		rest, sample, rerr := resyncFLAC(rc)
		if rerr != nil {
			rc.Close()
			return nil, rerr
		}
		src, err := openReader(&splicedRC{r: io.MultiReader(bytes.NewReader(header), rest), c: rc}, ext)
		if err != nil {
			rc.Close()
			return nil, err
		}
		// The frame header said exactly which sample it starts at, so unlike
		// mp3 this position is not an estimate. base is the DIFFERENCE from
		// what the decoder already accounts for, not the raw sample: beep's
		// flac decoder was observed priming its position from that same frame
		// header, and adding the sample on top would double-count it — while
		// assuming it always will is a bet on undocumented behaviour. The
		// subtraction is correct either way.
		src.base = int(sample) - src.streamer.Position()
		return src, nil
	}
}

// splicedRC glues the re-served metadata header onto the mid-stream frames and
// closes the network stream underneath.
type splicedRC struct {
	r io.Reader
	c io.Closer
}

func (s *splicedRC) Read(b []byte) (int, error) { return s.r.Read(b) }
func (s *splicedRC) Close() error               { return s.c.Close() }

// readFLACHeader reads the stream marker and every metadata block — the part
// of a FLAC file the parser refuses to live without — and returns the raw
// bytes, positioned so that what follows is the first audio frame.
func readFLACHeader(r io.Reader) ([]byte, error) {
	// Headers are small even with embedded cover art; a walk that claims more
	// than this is a corrupt length field, not a big picture.
	const maxHeader = 16 << 20

	out := make([]byte, 4)
	if _, err := io.ReadFull(r, out); err != nil {
		return nil, err
	}
	if string(out) != "fLaC" {
		return nil, errors.New("not a FLAC stream")
	}
	for {
		var bh [4]byte
		if _, err := io.ReadFull(r, bh[:]); err != nil {
			return nil, err
		}
		out = append(out, bh[:]...)
		length := int(bh[1])<<16 | int(bh[2])<<8 | int(bh[3])
		if len(out)+length > maxHeader {
			return nil, errors.New("FLAC metadata claims an unreasonable size")
		}
		body := make([]byte, length)
		if _, err := io.ReadFull(r, body); err != nil {
			return nil, err
		}
		out = append(out, body...)
		if bh[0]&0x80 != 0 { // the is-last flag
			return out, nil
		}
	}
}

const (
	// flacScanWindow bounds the resync scan: a real frame boundary appears
	// within a frame's length of any offset, so needing more than this means
	// the bytes are not the FLAC they claim to be. Generous, because it is
	// only a stop against garbage — the common case finds a frame in the
	// first chunk.
	flacScanWindow = 4 << 20
	// flacFrameSlack is how many bytes past a sync candidate must be in hand
	// before a parse FAILURE condemns it — a frame cut short by the buffer
	// looks corrupt without being wrong.
	flacFrameSlack = 256 << 10
	flacScanChunk  = 128 << 10
)

// resyncFLAC finds the first real frame boundary in a stream cut at an
// arbitrary byte offset. A candidate is two sync bytes; a WINNER is a
// candidate mewkiz parses as a complete frame (header CRC-8 and all), whose
// header then names the exact sample it starts at.
func resyncFLAC(r io.Reader) (io.Reader, int64, error) {
	buf := make([]byte, 0, flacScanChunk)
	from := 0
	eof := false
	for {
		if !eof {
			chunk := make([]byte, flacScanChunk)
			n, err := io.ReadFull(r, chunk)
			buf = append(buf, chunk[:n]...)
			if err != nil {
				eof = true
			}
		}
		i := from
		for ; i+2 <= len(buf); i++ {
			if buf[i] != 0xFF || buf[i+1]&0xFC != 0xF8 {
				continue
			}
			if !eof && len(buf)-i < flacFrameSlack {
				// Too close to the edge to judge; resume here with more bytes.
				break
			}
			fr, err := frame.New(bytes.NewReader(buf[i:]))
			if err != nil {
				continue
			}
			return io.MultiReader(bytes.NewReader(buf[i:]), r), int64(fr.SampleNumber()), nil
		}
		from = i
		if eof {
			return nil, 0, errors.New("no FLAC frame boundary found in the seeked region")
		}
		if len(buf) >= flacScanWindow {
			return nil, 0, errors.New("no FLAC frame boundary within the scan window")
		}
	}
}
