package probe

import (
	"errors"
	"io"

	"github.com/mewkiz/flac"
)

// FLAC states everything in one block, so there is nothing to estimate: the
// STREAMINFO at the front of every stream carries the rate, the channel count,
// the sample depth and the total sample count.
func inspectFLAC(r io.ReadSeeker, size int64) (*Info, error) {
	s, err := flac.New(r)
	if err != nil {
		return nil, err
	}
	defer s.Close()

	si := s.Info
	if si == nil {
		return nil, errors.New("probe: FLAC stream has no STREAMINFO")
	}
	info := &Info{
		Codec:      "flac",
		SampleRate: int(si.SampleRate),
		Channels:   int(si.NChannels),
		BitDepth:   int(si.BitsPerSample),
	}
	// NSamples is optional — a stream written without knowing its own length
	// leaves it zero, which is the honest answer rather than a computed one.
	if si.NSamples > 0 && si.SampleRate > 0 {
		info.DurationSeconds = float64(si.NSamples) / float64(si.SampleRate)
		info.Bitrate = bitrateFrom(size, info.DurationSeconds)
	}
	return info, nil
}
