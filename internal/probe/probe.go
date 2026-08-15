// Package probe reads an audio file's technical facts out of its container,
// without decoding it and without ffprobe.
//
// It is the other half of what madshare's analysis shells out for: ffprobe
// fills the codec, bitrate, sample rate, channel and bit-depth columns, and on
// a phone there is no ffprobe and no way to run one. Those columns are what the
// quality ladder ranks renditions by, so a device without them publishes a
// catalog its friends cannot compare — which is exactly the case a player on a
// phone is in.
//
// Everything here reads headers only: a few hundred bytes at the front, and for
// Ogg a few at the back. That is a deliberate ceiling. A scan enqueues one of
// these per file and a person is waiting for their library to appear, so no
// format may cost a walk over the whole file.
//
// # What it does not do
//
// It reports what the container declares. Where a container declares nothing —
// an MP3 with no Xing header, whose length is a guess from its bitrate — the
// number is an estimate, and that is what ffprobe reports too.
package probe

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
)

// Info is the tech facts, shaped to fill madshare's columns. A zero field means
// the container did not say, and is persisted as NULL rather than as zero.
type Info struct {
	Codec           string  // "mp3", "flac", "vorbis", "opus", "aac", "alac", "pcm_s16le"…
	DurationSeconds float64 //
	Bitrate         int     // bits per second, averaged over the file
	SampleRate      int     // Hz
	Channels        int     //
	BitDepth        int     // bits per sample; 0 for lossy formats, which have none

	// SkipSamples is the leading audio a correct decoder discards: an encoder's
	// priming samples, which are real samples in the file that were never in the
	// recording. Only MP3 declares it in a way anything here can read.
	//
	// It is not a tech column. It is here because this is the only code that
	// parses the container, and the fingerprinter needs it: ffmpeg drops these
	// samples and the pure-Go MP3 decoder does not, so a fingerprint taken
	// without dropping them is offset against every fingerprint fpcalc ever
	// made of the same file.
	SkipSamples int
}

// Inspect reads path's container and reports what it declares.
//
// The extension picks the parser, as it does everywhere else in this program: a
// scanner that already decided this file is audio named it, and sniffing would
// only disagree with the decoder that plays it.
func Inspect(path string) (*Info, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	size, err := f.Seek(0, os.SEEK_END)
	if err != nil {
		return nil, err
	}
	if _, err := f.Seek(0, os.SEEK_SET); err != nil {
		return nil, err
	}

	switch strings.ToLower(filepath.Ext(path)) {
	case ".mp3":
		return inspectMP3(f, size)
	case ".flac":
		return inspectFLAC(f, size)
	case ".wav", ".wave":
		return inspectWAV(f, size)
	case ".ogg", ".oga", ".opus":
		return inspectOgg(f, size)
	case ".m4a", ".m4b", ".mp4", ".aac":
		return inspectMP4(f, size)
	}
	return nil, fmt.Errorf("probe: no reader for %s", filepath.Ext(path))
}

// bitrateFrom fills in an average bitrate from the file's size, for containers
// that state a duration but no rate. It is what the number means anyway: total
// bits over total seconds, headers and tags included.
// It rounds rather than truncates, because ffmpeg's av_rescale does and a
// column differing by one from the server's would look like a real difference.
func bitrateFrom(size int64, seconds float64) int {
	if seconds <= 0 || size <= 0 {
		return 0
	}
	return int(math.Round(float64(size) * 8 / seconds))
}
