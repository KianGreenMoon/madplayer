// Package player owns playback: decoding a file, the position within it, and
// driving the queue when a track ends.
//
// The audio OUTPUT is injected as a Sink, so everything here — decode, seek,
// position, queue advance — builds and tests on a machine with no sound card,
// and the one package that needs an audio device stays at the edge.
package player

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/gopxl/beep/v2"
	"github.com/gopxl/beep/v2/flac"
	"github.com/gopxl/beep/v2/mp3"
	"github.com/gopxl/beep/v2/vorbis"
	"github.com/gopxl/beep/v2/wav"
)

// decodable maps an extension to its decoder.
//
// This is deliberately SHORTER than the scanner's accepted list. m4a/mp4/aac and
// opus are indexed — the server accepts them, so a library full of them should
// not look empty — but nothing here decodes them yet: they need cgo bindings or
// ffmpeg, which docs/ui/madplayer.md lists under the native client's own
// burdens. Showing such a track and saying it cannot be played is honest;
// hiding it would look like the file is missing.
// The decoders disagree about their parameter type — mp3 and vorbis take an
// io.ReadCloser, flac and wav an io.Reader — so each is wrapped to a common
// signature over io.ReadCloser, which satisfies both and, unlike *os.File, does
// not force the source to be seekable.
var decodable = map[string]func(io.ReadCloser) (beep.StreamSeekCloser, beep.Format, error){
	".mp3":  func(r io.ReadCloser) (beep.StreamSeekCloser, beep.Format, error) { return mp3.Decode(r) },
	".flac": func(r io.ReadCloser) (beep.StreamSeekCloser, beep.Format, error) { return flac.Decode(r) },
	".wav":  func(r io.ReadCloser) (beep.StreamSeekCloser, beep.Format, error) { return wav.Decode(r) },
	".ogg":  func(r io.ReadCloser) (beep.StreamSeekCloser, beep.Format, error) { return vorbis.Decode(r) },
	".oga":  func(r io.ReadCloser) (beep.StreamSeekCloser, beep.Format, error) { return vorbis.Decode(r) },
}

// Decodable reports whether this build can play the file.
func Decodable(path string) bool {
	_, ok := decodable[strings.ToLower(filepath.Ext(path))]
	return ok
}

// ErrUnsupported is returned for a file this build has no decoder for. It is a
// distinct error because the UI says something different about it than about a
// file that is missing or corrupt.
type ErrUnsupported struct{ Ext string }

func (e *ErrUnsupported) Error() string {
	return fmt.Sprintf("no decoder for %s files in this build", e.Ext)
}

// source is an open, decoded stream of audio.
type source struct {
	streamer beep.StreamSeekCloser
	format   beep.Format
	closer   io.Closer
	// seekable is false while the bytes are still arriving.
	//
	// It is not a nicety: beep's mp3 decoder PANICS if Seek is called on a
	// non-seekable source ("The Seek method will panic if rc is not io.Seeker"),
	// so this is what stands between a scrub during a download and the program
	// dying in the audio path.
	seekable bool
	// base is the sample this source's byte stream actually starts at, for a
	// source opened MID-track (rangeseek.go): the decoder counts from zero
	// because zero is all it saw, and base is what makes Position read as the
	// track, not the stream. Exact for flac (the frame header names its
	// sample), the byte-fraction estimate for mp3 — a browser's accuracy.
	base int
}

func (s *source) Close() error {
	if s == nil {
		return nil
	}
	err := s.streamer.Close()
	// Some decoders take ownership of the reader and close it themselves, so a
	// second close here is expected and its error is not news.
	if s.closer != nil {
		_ = s.closer.Close()
	}
	return err
}

// open decodes a file for playback. The file seeks, so everything downstream
// can.
func open(path string) (*source, error) {
	// The extension is checked BEFORE the file is opened, so a container this
	// build cannot decode reports that rather than whatever the filesystem says.
	// The UI tells "cannot be played" from "not on this device right now" in
	// different words, and a missing m4a must not claim to be the second.
	ext := strings.ToLower(filepath.Ext(path))
	if _, ok := decodable[ext]; !ok {
		return nil, &ErrUnsupported{Ext: ext}
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	src, err := openReader(f, ext)
	if err != nil {
		f.Close()
		return nil, err
	}
	return src, nil
}

// openReader decodes whatever a reader hands over, which may be a file that is
// still being written.
//
// A reader that does not seek is what makes streaming work at all: go-mp3 skips
// its whole-file length walk when its source is not an io.Seeker, and beep's
// flac picks its non-seeking parser for the same reason. Measured on real files,
// a decoder is ready after 0.0%–0.2% of the bytes instead of 100% of them.
//
// The extension still decides the decoder — there is no sniffing here, and the
// name is all a growing file has to go on.
func openReader(rc io.ReadCloser, ext string) (*source, error) {
	ext = strings.ToLower(ext)
	dec, ok := decodable[ext]
	if !ok {
		return nil, &ErrUnsupported{Ext: ext}
	}
	streamer, format, err := dec(rc)
	if err != nil {
		return nil, err
	}
	_, seekable := rc.(io.Seeker)
	return &source{streamer: streamer, format: format, closer: rc, seekable: seekable}, nil
}

// Probe returns a file's duration in seconds by decoding just its header.
//
// This is the native client doing properly what the web UI can only approximate:
// docs/ui/library-page.md notes that a row with no `duration_seconds` renders
// "—" and the browser then loads the media to find out. A real decoder answers
// directly. The rule it keeps is the important one — the row renders IMMEDIATELY
// with "—" and the length arrives later, rather than the list waiting on a walk
// over every file.
func Probe(path string) (float64, error) {
	src, err := open(path)
	if err != nil {
		return 0, err
	}
	defer src.Close()

	n := src.streamer.Len()
	if n <= 0 || src.format.SampleRate <= 0 {
		return 0, fmt.Errorf("%s: decoder reported no length", filepath.Base(path))
	}
	return float64(n) / float64(src.format.SampleRate), nil
}
