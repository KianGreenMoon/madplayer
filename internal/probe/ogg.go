package probe

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
)

// Ogg is the one format here that needs both ends of the file.
//
// The first page carries the codec's identification header — rate and channels
// — and nothing anywhere states the length. What the LAST page carries is a
// granule position: the sample number one past the end. So this reads the front
// and then the final few kilobytes, which is still bounded and still not a walk.

// oggTailScan is how far back to look for the last page header. A page holds at
// most 255 segments of 255 bytes, so 64 KiB always contains at least one page
// boundary.
const oggTailScan = 1 << 16

func inspectOgg(r io.ReadSeeker, size int64) (*Info, error) {
	head := make([]byte, 4096)
	n, err := io.ReadFull(r, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return nil, err
	}
	head = head[:n]
	if len(head) < 27 || string(head[:4]) != "OggS" {
		return nil, errors.New("probe: not an Ogg stream")
	}

	body, err := firstPacket(head)
	if err != nil {
		return nil, err
	}

	info := &Info{}
	// The granule clock differs per codec: Vorbis counts in output samples, Opus
	// always counts at 48 kHz whatever it decodes to. Getting this wrong is a
	// duration wrong by the ratio of the two.
	granuleRate := 0
	switch {
	case len(body) >= 30 && bytes.HasPrefix(body, []byte{1, 'v', 'o', 'r', 'b', 'i', 's'}):
		info.Codec = "vorbis"
		info.Channels = int(body[11])
		info.SampleRate = int(binary.LittleEndian.Uint32(body[12:16]))
		// The nominal bitrate; the real one is computed from the size below.
		info.Bitrate = int(int32(binary.LittleEndian.Uint32(body[20:24])))
		granuleRate = info.SampleRate
	case len(body) >= 19 && bytes.HasPrefix(body, []byte("OpusHead")):
		info.Codec = "opus"
		info.Channels = int(body[9])
		// Opus decodes to whatever you ask for; the header states the rate the
		// material was captured at, which is what ffprobe reports.
		info.SampleRate = int(binary.LittleEndian.Uint32(body[12:16]))
		if info.SampleRate == 0 {
			info.SampleRate = 48000
		}
		granuleRate = 48000
	case len(body) >= 28 && bytes.HasPrefix(body, []byte("\x7fFLAC")):
		// FLAC in Ogg: the native STREAMINFO follows the small wrapper header.
		si := body[13:]
		if len(si) >= 34 {
			info.Codec = "flac"
			info.SampleRate = int(si[10])<<12 | int(si[11])<<4 | int(si[12])>>4
			info.Channels = int(si[12]>>1&0x07) + 1
			info.BitDepth = int(si[12]&0x01)<<4 | int(si[13]>>4) + 1
			granuleRate = info.SampleRate
		}
	default:
		return nil, errors.New("probe: unrecognised Ogg codec")
	}

	if g, ok := lastGranule(r, size); ok && granuleRate > 0 {
		info.DurationSeconds = float64(g) / float64(granuleRate)
		info.Bitrate = bitrateFrom(size, info.DurationSeconds)
	}
	return info, nil
}

// firstPacket returns the payload of the first page, which by the container's
// own rule holds the identification header alone.
func firstPacket(page []byte) ([]byte, error) {
	if len(page) < 27 {
		return nil, errors.New("probe: truncated Ogg page")
	}
	segments := int(page[26])
	if len(page) < 27+segments {
		return nil, errors.New("probe: truncated Ogg segment table")
	}
	var length int
	for _, l := range page[27 : 27+segments] {
		length += int(l)
	}
	start := 27 + segments
	if start+length > len(page) {
		length = len(page) - start
	}
	return page[start : start+length], nil
}

// lastGranule finds the final page's granule position: the sample number one
// past the end of the stream, and so its length.
func lastGranule(r io.ReadSeeker, size int64) (uint64, bool) {
	from := size - oggTailScan
	if from < 0 {
		from = 0
	}
	if _, err := r.Seek(from, io.SeekStart); err != nil {
		return 0, false
	}
	tail, err := io.ReadAll(io.LimitReader(r, size-from))
	if err != nil {
		return 0, false
	}
	// Scan forward keeping the last plausible header rather than searching
	// backwards: "OggS" can occur inside packet data, and the last MATCH that is
	// also a page start is the one at the end.
	var granule uint64
	var found bool
	for i := 0; i+27 <= len(tail); i++ {
		if tail[i] != 'O' || string(tail[i:i+4]) != "OggS" {
			continue
		}
		g := binary.LittleEndian.Uint64(tail[i+6 : i+14])
		// -1 marks a page that completes no packet, and carries no position.
		if g != ^uint64(0) {
			granule, found = g, true
		}
	}
	return granule, found
}
