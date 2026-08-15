package probe

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// WAV is a chunk list, and the two chunks that matter are fmt (the format) and
// data (how much of it there is). Walking the list is cheap because each chunk
// declares its own length, so nothing is read but the headers.

// wavMaxChunks bounds the walk. A well-formed file reaches data in two or three
// chunks; a malformed one must not become an infinite loop inside a scan.
const wavMaxChunks = 64

func inspectWAV(r io.ReadSeeker, size int64) (*Info, error) {
	var riff [12]byte
	if _, err := io.ReadFull(r, riff[:]); err != nil {
		return nil, err
	}
	if string(riff[0:4]) != "RIFF" || string(riff[8:12]) != "WAVE" {
		return nil, errors.New("probe: not a RIFF/WAVE file")
	}

	info := &Info{}
	var blockAlign int
	var haveFmt bool

	for i := 0; i < wavMaxChunks; i++ {
		var hdr [8]byte
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			break
		}
		id := string(hdr[0:4])
		length := int64(binary.LittleEndian.Uint32(hdr[4:8]))

		switch id {
		case "fmt ":
			body := make([]byte, min64(length, 40))
			if _, err := io.ReadFull(r, body); err != nil {
				return nil, err
			}
			if len(body) < 16 {
				return nil, errors.New("probe: WAV fmt chunk is too short")
			}
			format := binary.LittleEndian.Uint16(body[0:2])
			info.Channels = int(binary.LittleEndian.Uint16(body[2:4]))
			info.SampleRate = int(binary.LittleEndian.Uint32(body[4:8]))
			info.Bitrate = int(binary.LittleEndian.Uint32(body[8:12])) * 8
			blockAlign = int(binary.LittleEndian.Uint16(body[12:14]))
			info.BitDepth = int(binary.LittleEndian.Uint16(body[14:16]))
			// WAVE_FORMAT_EXTENSIBLE hides the real format in a GUID whose first
			// two bytes are the tag it stands for.
			if format == 0xFFFE && len(body) >= 40 {
				format = binary.LittleEndian.Uint16(body[24:26])
			}
			info.Codec = wavCodec(format, info.BitDepth)
			haveFmt = true
			if skip := length - int64(len(body)); skip > 0 {
				if _, err := r.Seek(skip, io.SeekCurrent); err != nil {
					return nil, err
				}
			}
		case "data":
			if !haveFmt {
				return nil, errors.New("probe: WAV data chunk before fmt")
			}
			// A streamed file can declare a nonsense length (0, or 0xFFFFFFFF)
			// because the writer did not know it yet; the file's own size is the
			// better answer then.
			pos, err := r.Seek(0, io.SeekCurrent)
			if err != nil {
				return nil, err
			}
			if length <= 0 || pos+length > size {
				length = size - pos
			}
			if info.Bitrate > 0 {
				info.DurationSeconds = float64(length) * 8 / float64(info.Bitrate)
			} else if blockAlign > 0 && info.SampleRate > 0 {
				info.DurationSeconds = float64(length/int64(blockAlign)) / float64(info.SampleRate)
			}
			return info, nil
		default:
			// Chunks are padded to even lengths, and the pad byte is not counted.
			if _, err := r.Seek(length+length&1, io.SeekCurrent); err != nil {
				return nil, err
			}
		}
	}
	if haveFmt {
		return info, nil // format known, length not: better than nothing
	}
	return nil, errors.New("probe: WAV has no fmt chunk")
}

// wavCodec names the sample format the way ffprobe names it, since these
// strings land in a column two clients read.
func wavCodec(format uint16, depth int) string {
	switch format {
	case 1:
		switch depth {
		case 8:
			return "pcm_u8"
		case 16, 24, 32:
			return fmt.Sprintf("pcm_s%dle", depth)
		}
		return "pcm"
	case 3:
		if depth == 64 {
			return "pcm_f64le"
		}
		return "pcm_f32le"
	case 6:
		return "pcm_alaw"
	case 7:
		return "pcm_mulaw"
	case 0x0055:
		return "mp3"
	}
	return fmt.Sprintf("wav_0x%04x", format)
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
