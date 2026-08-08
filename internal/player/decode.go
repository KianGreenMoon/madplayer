// Package player owns playback: decoding a file, the position within it, and
// driving the queue when a track ends.
//
// The audio OUTPUT is injected as a Sink, so everything here — decode, seek,
// position, queue advance — builds and tests on a machine with no sound card,
// and the one package that needs an audio device stays at the edge.
package player

import (
	"fmt"
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
// signature over *os.File, which satisfies both.
var decodable = map[string]func(*os.File) (beep.StreamSeekCloser, beep.Format, error){
	".mp3":  func(f *os.File) (beep.StreamSeekCloser, beep.Format, error) { return mp3.Decode(f) },
	".flac": func(f *os.File) (beep.StreamSeekCloser, beep.Format, error) { return flac.Decode(f) },
	".wav":  func(f *os.File) (beep.StreamSeekCloser, beep.Format, error) { return wav.Decode(f) },
	".ogg":  func(f *os.File) (beep.StreamSeekCloser, beep.Format, error) { return vorbis.Decode(f) },
	".oga":  func(f *os.File) (beep.StreamSeekCloser, beep.Format, error) { return vorbis.Decode(f) },
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

// source is an open, decoded file.
type source struct {
	streamer beep.StreamSeekCloser
	format   beep.Format
	file     *os.File
}

func (s *source) Close() error {
	if s == nil {
		return nil
	}
	err := s.streamer.Close()
	// Some decoders take ownership of the file and close it themselves, so a
	// second close here is expected and its error is not news.
	_ = s.file.Close()
	return err
}

// open decodes a file for playback.
func open(path string) (*source, error) {
	ext := strings.ToLower(filepath.Ext(path))
	dec, ok := decodable[ext]
	if !ok {
		return nil, &ErrUnsupported{Ext: ext}
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	streamer, format, err := dec(f)
	if err != nil {
		f.Close()
		return nil, err
	}
	return &source{streamer: streamer, format: format, file: f}, nil
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
