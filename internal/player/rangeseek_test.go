package player

// Seeking a still-arriving track must fetch the SEEKED region first — the way
// the web UI's browser hits the relay with a Range request — never wait for
// the sequential download to crawl there. The proof here is structural: the
// fill is capped below the seek target and NEVER released, so the only way the
// position can land is the range path.
//
// FLAC carries the whole test load because it is the only decodable format Go
// can synthesize hermetically (mewkiz encodes; nothing here encodes mp3). The
// mp3 half of rangeseek.go rests on go-mp3's own resync loop — its frame
// reader slides byte-by-byte to the next valid sync, verified in its source —
// and shares every other line with the flac path.

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/mewkiz/flac"
	"github.com/mewkiz/flac/frame"
	"github.com/mewkiz/flac/meta"

	"daemonlord.ygg/madplayer/internal/queue"
)

const (
	flacBlock  = 4096
	flacBlocks = 40 // ~3.7s at 44.1kHz
)

// writeFLAC synthesises a real, decodable FLAC of silence — hermetic for the
// same reason writeWAV is: no fixture audio, no licence, exact length known.
func writeFLAC(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	info := &meta.StreamInfo{
		BlockSizeMin: flacBlock, BlockSizeMax: flacBlock,
		SampleRate: 44100, NChannels: 1, BitsPerSample: 16,
		NSamples: flacBlock * flacBlocks,
	}
	enc, err := flac.NewEncoder(f, info)
	if err != nil {
		t.Fatal(err)
	}
	samples := make([]int32, flacBlock)
	for i := 0; i < flacBlocks; i++ {
		fr := &frame.Frame{
			Header: frame.Header{
				HasFixedBlockSize: true,
				BlockSize:         flacBlock,
				SampleRate:        44100,
				Channels:          frame.ChannelsMono,
				BitsPerSample:     16,
				Num:               uint64(i),
			},
			Subframes: []*frame.Subframe{{
				SubHeader: frame.SubHeader{Pred: frame.PredConstant},
				Samples:   samples,
				NSamples:  flacBlock,
			}},
		}
		if err := enc.WriteFrame(fr); err != nil {
			t.Fatal(err)
		}
	}
	if err := enc.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

// rangedFetcher streams through a gate stuck at limit bytes, but serves any
// byte range instantly — a fill that cannot reach the seek target beside a
// relay that can.
type rangedFetcher struct {
	path  string
	limit int64
	open  chan struct{} // never closed in these tests: the fill stays stuck
}

func (s *rangedFetcher) Local(context.Context, *queue.Item) (string, error) {
	return s.path, nil
}

func (s *rangedFetcher) Stream(context.Context, *queue.Item) (io.ReadCloser, string, error) {
	f, err := os.Open(s.path)
	if err != nil {
		return nil, "", err
	}
	return &gated{f: f, limit: s.limit, open: s.open}, filepath.Ext(s.path), nil
}

func (s *rangedFetcher) TrackSize(context.Context, *queue.Item) (int64, error) {
	fi, err := os.Stat(s.path)
	if err != nil {
		return 0, err
	}
	return fi.Size(), nil
}

func (s *rangedFetcher) OpenRange(_ context.Context, _ *queue.Item, offset int64) (io.ReadCloser, error) {
	b, err := os.ReadFile(s.path)
	if err != nil {
		return nil, err
	}
	if offset > int64(len(b)) {
		offset = int64(len(b))
	}
	return io.NopCloser(bytes.NewReader(b[offset:])), nil
}

// The headline: the fill is stuck at half the file and is never released, and
// a seek to two thirds still lands — because the seeked region was fetched
// first, over the range surface, exactly like the web UI against the relay.
func TestASeekFetchesTheSeekedPartFirst(t *testing.T) {
	dir := t.TempDir()
	path := writeFLAC(t, dir, "a.flac")
	fi, _ := os.Stat(path)

	fetch := &rangedFetcher{path: path, limit: fi.Size() / 2, open: make(chan struct{})}
	p, err := New(&fakeSink{})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	p.SetFetcher(fetch)

	p.SetQueue([]*queue.Item{{URL: "https://elsewhere/a.flac", Title: "streamed"}}, 0)
	waitPlaying(t, p)

	p.Seek(2.5) // frac ≈ 0.67 of the bytes — past the stuck fill
	waitFor(t, "the seek to land ahead of the download", func() bool {
		elapsed, _ := p.Position()
		return elapsed >= 2.4 && elapsed <= 2.9
	})
	if !p.Playing() {
		t.Error("the track stopped playing across the seek")
	}
	if _, total := p.Position(); total < 3.5 || total > 3.9 {
		t.Errorf("total = %.2f, want the STREAMINFO length (~3.7s)", total)
	}
}

// The landing position is EXACT for flac: the resynced frame's header names
// the sample it starts at, so Position reports the truth rather than the
// byte-fraction guess the request was aimed with.
func TestAFlacRangeSeekLandsOnAFrameBoundary(t *testing.T) {
	dir := t.TempDir()
	path := writeFLAC(t, dir, "a.flac")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	rest, sample, err := resyncFLAC(bytes.NewReader(b[len(b)/2:]))
	if err != nil {
		t.Fatalf("resync: %v", err)
	}
	if sample <= 0 || sample%flacBlock != 0 {
		t.Errorf("resynced to sample %d, want a positive multiple of the block size %d", sample, flacBlock)
	}
	if rest == nil {
		t.Fatal("no remainder reader")
	}
}

// The metadata walk hands back exactly the header — what the spliced stream
// puts in front of the mid-file frames so the parser accepts them.
func TestReadFLACHeaderStopsAtTheFirstFrame(t *testing.T) {
	dir := t.TempDir()
	path := writeFLAC(t, dir, "a.flac")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	header, err := readFLACHeader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(header, b[:len(header)]) {
		t.Fatal("header bytes differ from the file's own prefix")
	}
	// What follows the header must be a frame boundary: sync code first.
	rest := b[len(header):]
	if len(rest) < 2 || rest[0] != 0xFF || rest[1]&0xFC != 0xF8 {
		t.Fatalf("the walk stopped at % x, not at a frame sync", rest[:2])
	}
}

// A fetcher without the range surface falls back to decoding through the
// sequential fill — the seek is slower, never lost. (The gated fetcher in
// seekstream_test.go covers the wait-for-the-bytes behaviour; this pins only
// that the assertion of RangeFetcher is genuinely optional.)
func TestAFetcherWithoutRangesStillSeeks(t *testing.T) {
	dir := t.TempDir()
	path := writeWAV(t, dir, "a.wav", 3)

	p, err := New(&fakeSink{})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	p.SetFetcher(&streamFetcher{path: path, stop: make(chan struct{})})

	p.SetQueue([]*queue.Item{{URL: "https://elsewhere/a.wav"}}, 0)
	waitPlaying(t, p)

	p.Seek(1.5)
	waitFor(t, "the fallback seek to land", func() bool {
		elapsed, _ := p.Position()
		return elapsed >= 1.4 && elapsed <= 1.7
	})
}
