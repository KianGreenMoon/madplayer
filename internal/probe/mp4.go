package probe

import (
	"encoding/binary"
	"errors"
	"io"
)

// MP4 and its audio-only spelling, M4A: a tree of length-prefixed boxes.
//
// This walks only the path it needs — moov ▸ mvhd for the duration, then
// moov ▸ trak ▸ mdia ▸ minf ▸ stbl ▸ stsd for the codec and its parameters —
// and steps over everything else by its declared length, so the audio itself is
// never read.
//
// These files are indexed but NOT decodable by this build, which is exactly why
// probing them matters: the row says what the track is and why it cannot be
// played, and a row with no facts at all cannot say either.

// mp4MaxDepth bounds the descent. Real files nest six deep; a crafted one must
// not recurse until the stack gives out.
const mp4MaxDepth = 8

func inspectMP4(r io.ReadSeeker, size int64) (*Info, error) {
	info := &Info{}
	var timescale, duration uint64
	if err := walkBoxes(r, size, 0, func(kind string, body io.ReadSeeker, length int64) error {
		switch kind {
		case "mvhd":
			ts, d, err := readMVHD(body, length)
			if err != nil {
				return err
			}
			timescale, duration = ts, d
		case "stsd":
			return readSTSD(body, length, info)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	if info.Codec == "" {
		return nil, errors.New("probe: no audio sample description in MP4")
	}
	if timescale > 0 {
		info.DurationSeconds = float64(duration) / float64(timescale)
		info.Bitrate = bitrateFrom(size, info.DurationSeconds)
	}
	return info, nil
}

// containers are the boxes worth descending into. Everything else is skipped
// whole, which is what keeps this bounded.
var containers = map[string]bool{
	"moov": true, "trak": true, "mdia": true, "minf": true, "stbl": true,
}

// walkBoxes calls visit for each leaf box of interest, descending only through
// the containers above.
func walkBoxes(r io.ReadSeeker, end int64, depth int, visit func(string, io.ReadSeeker, int64) error) error {
	if depth > mp4MaxDepth {
		return nil
	}
	for {
		pos, err := r.Seek(0, io.SeekCurrent)
		if err != nil {
			return err
		}
		if pos+8 > end {
			return nil
		}
		var hdr [8]byte
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			return nil // a truncated tail is not worth failing the whole probe
		}
		size := int64(binary.BigEndian.Uint32(hdr[0:4]))
		kind := string(hdr[4:8])
		header := int64(8)
		switch {
		case size == 1:
			// A 64-bit length follows the header, for boxes over 4 GiB.
			var ext [8]byte
			if _, err := io.ReadFull(r, ext[:]); err != nil {
				return nil
			}
			size = int64(binary.BigEndian.Uint64(ext[:]))
			header = 16
		case size == 0:
			size = end - pos // "to the end of the file"
		}
		if size < header || pos+size > end {
			return nil
		}

		if containers[kind] {
			if err := walkBoxes(r, pos+size, depth+1, visit); err != nil {
				return err
			}
		} else if err := visit(kind, r, size-header); err != nil {
			return err
		}
		if _, err := r.Seek(pos+size, io.SeekStart); err != nil {
			return err
		}
	}
}

// readMVHD takes the movie header's timescale and duration, which together are
// the only length an MP4 states in one place.
func readMVHD(r io.Reader, length int64) (timescale, duration uint64, err error) {
	body := make([]byte, min64(length, 32))
	if _, err := io.ReadFull(r, body); err != nil {
		return 0, 0, nil
	}
	if len(body) < 20 {
		return 0, 0, nil
	}
	// Version 1 widened the times to 64 bits; the field order is otherwise the
	// same, after the version/flags word and the two creation times.
	if body[0] == 1 {
		if len(body) < 32 {
			return 0, 0, nil
		}
		return uint64(binary.BigEndian.Uint32(body[20:24])), binary.BigEndian.Uint64(body[24:32]), nil
	}
	return uint64(binary.BigEndian.Uint32(body[12:16])), uint64(binary.BigEndian.Uint32(body[16:20])), nil
}

// readSTSD reads the first sample description, which for an audio track names
// the codec and states its rate and channel count.
func readSTSD(r io.Reader, length int64, info *Info) error {
	body := make([]byte, min64(length, 256))
	if _, err := io.ReadFull(r, body); err != nil {
		return nil
	}
	if len(body) < 44 {
		return nil
	}
	// version+flags (4), entry count (4), then the entry: its own size (4) and
	// four-character format (4).
	entry := body[8:]
	format := string(entry[4:8])
	// An audio sample entry: 6 reserved bytes, 2 data-reference index, 8 more
	// reserved, then channels, sample size and the rate as 16.16 fixed point.
	const audioFields = 8 + 8 + 8
	if len(entry) < audioFields+8 {
		return nil
	}
	f := entry[audioFields:]
	info.Channels = int(binary.BigEndian.Uint16(f[0:2]))
	if depth := int(binary.BigEndian.Uint16(f[2:4])); depth > 0 && depth != 16 {
		// Only worth reporting where it is not the format's default, which is
		// what ffprobe does: a lossy codec has no meaningful sample depth.
		info.BitDepth = depth
	}
	info.SampleRate = int(binary.BigEndian.Uint32(f[6:10]) >> 16)

	switch format {
	case "mp4a":
		info.Codec = "aac"
		info.BitDepth = 0
	case "alac":
		info.Codec = "alac"
	case "Opus":
		info.Codec = "opus"
		info.BitDepth = 0
	case "fLaC":
		info.Codec = "flac"
	case ".mp3", "mp3 ":
		info.Codec = "mp3"
		info.BitDepth = 0
	default:
		info.Codec = format
	}
	return nil
}
