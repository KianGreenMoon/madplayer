package probe

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// The builders behind probe_test.go: just enough of each container for its
// reader to have something true to read.

type mp3Spec struct {
	frames      int  // as declared by the Xing header
	bytes       int  // as declared by the Xing header
	startPad    int  // encoder priming
	endPad      int  //
	cbr         bool // write "Info" rather than "Xing"
	noLAME      bool // omit the encoder extension that carries the pads
	mono        bool //
	mpeg2       bool // a low sampling frequency version: half-length frames
	id3         int  // bytes of ID3v2 tag to hide the first frame behind
	plainFrames int  // audio frames with no Xing header at all
}

// mp3File writes an ID3 tag, a Xing/Info header frame and some audio frames.
func mp3File(t *testing.T, s mp3Spec) []byte {
	t.Helper()
	var b bytes.Buffer
	if s.id3 > 0 {
		// A syncsafe length, then that many bytes of nothing. It exists to prove
		// the frame search steps over a tag rather than finding a sync in it.
		size := s.id3
		b.Write([]byte{'I', 'D', '3', 4, 0, 0,
			byte(size >> 21 & 0x7F), byte(size >> 14 & 0x7F), byte(size >> 7 & 0x7F), byte(size & 0x7F)})
		b.Write(make([]byte, size))
	}

	header, frameLen := mp3Header(s.mono, s.mpeg2)
	if s.plainFrames > 0 {
		for range s.plainFrames {
			b.Write(header)
			b.Write(make([]byte, frameLen-4))
		}
		return b.Bytes()
	}

	// The header frame: a normal frame whose payload is the Xing tag, sitting
	// where the side info would be.
	frame := make([]byte, frameLen)
	copy(frame, header)
	offsets := [2][2]int{{32, 17}, {17, 9}}
	lsf, mono := 0, 0
	if s.mpeg2 {
		lsf = 1
	}
	if s.mono {
		mono = 1
	}
	p := 4 + offsets[lsf][mono]

	tag := "Xing"
	if s.cbr {
		tag = "Info"
	}
	copy(frame[p:], tag)
	binary.BigEndian.PutUint32(frame[p+4:], 0x3) // frames and bytes present
	binary.BigEndian.PutUint32(frame[p+8:], uint32(s.frames))
	binary.BigEndian.PutUint32(frame[p+12:], uint32(s.bytes))
	if !s.noLAME {
		q := p + 16
		copy(frame[q:], "LAME3.98r")
		v := uint32(s.startPad)<<12 | uint32(s.endPad)
		frame[q+21] = byte(v >> 16)
		frame[q+22] = byte(v >> 8)
		frame[q+23] = byte(v)
	}
	b.Write(frame)

	// One more real frame, so the sync check has a successor to confirm against.
	b.Write(header)
	b.Write(make([]byte, frameLen-4))
	return b.Bytes()
}

// mp3Header builds a 128 kbit/s Layer III frame header and returns its length.
func mp3Header(mono, mpeg2 bool) ([]byte, int) {
	h := []byte{0xFF, 0xFB, 0x90, 0x00} // MPEG1 Layer3, 128 kbps, 44100, stereo
	rate, samples := 44100, 1152
	if mpeg2 {
		h[1] = 0xF3 // MPEG2
		// Bitrate index 8 is 64 kbit/s in the MPEG2 Layer III table, and the
		// index must agree with the length computed below or the successor-frame
		// check lands in the middle of a frame and rejects both.
		h[2] = 0x80
		rate = 22050
		samples = 576
	}
	if mono {
		h[3] |= 0xC0
	}
	bitrate := 128000
	if mpeg2 {
		bitrate = 64000
	}
	return h, samples / 8 * bitrate / rate
}

// flacFile writes the magic and a STREAMINFO block, which is all a FLAC stream
// has to declare before its frames.
func flacFile(rate, depth, channels int, samples uint64) []byte {
	var b bytes.Buffer
	b.WriteString("fLaC")
	b.Write([]byte{0x80, 0, 0, 34}) // last block, type 0 (STREAMINFO), 34 bytes

	si := make([]byte, 34)
	binary.BigEndian.PutUint16(si[0:], 4096) // min block size
	binary.BigEndian.PutUint16(si[2:], 4096) // max block size
	// min and max frame size may be zero: "unknown".
	//
	// Then a bit-packed run: 20 bits of sample rate, 3 of channels-1, 5 of
	// depth-1, 36 of total samples.
	packed := uint64(rate)<<44 | uint64(channels-1)<<41 | uint64(depth-1)<<36 | samples
	binary.BigEndian.PutUint64(si[10:], packed)
	b.Write(si)
	return b.Bytes()
}

// wavFile writes a RIFF header, a fmt chunk and a data chunk of the given size.
func wavFile(rate, depth, channels, dataLen int) []byte {
	var b bytes.Buffer
	byteRate := rate * channels * depth / 8
	b.WriteString("RIFF")
	binary.Write(&b, binary.LittleEndian, uint32(36+dataLen))
	b.WriteString("WAVEfmt ")
	binary.Write(&b, binary.LittleEndian, uint32(16))
	binary.Write(&b, binary.LittleEndian, uint16(1)) // PCM
	binary.Write(&b, binary.LittleEndian, uint16(channels))
	binary.Write(&b, binary.LittleEndian, uint32(rate))
	binary.Write(&b, binary.LittleEndian, uint32(byteRate))
	binary.Write(&b, binary.LittleEndian, uint16(channels*depth/8))
	binary.Write(&b, binary.LittleEndian, uint16(depth))
	b.WriteString("data")
	binary.Write(&b, binary.LittleEndian, uint32(dataLen))
	b.Write(make([]byte, dataLen))
	return b.Bytes()
}

func opusHead(channels, rate int) []byte {
	h := make([]byte, 19)
	copy(h, "OpusHead")
	h[8] = 1 // version
	h[9] = byte(channels)
	binary.LittleEndian.PutUint32(h[12:], uint32(rate))
	return h
}

func vorbisHead(channels, rate int) []byte {
	h := make([]byte, 30)
	copy(h, []byte{1, 'v', 'o', 'r', 'b', 'i', 's'})
	h[11] = byte(channels)
	binary.LittleEndian.PutUint32(h[12:], uint32(rate))
	binary.LittleEndian.PutUint32(h[20:], 128000) // nominal bitrate
	return h
}

// oggFile writes an identification page and a final page carrying the granule
// position that gives the stream its length.
func oggFile(head []byte, lastGranule uint64) []byte {
	var b bytes.Buffer
	b.Write(oggPage(head, 0, 0x02)) // beginning of stream
	b.Write(oggPage(make([]byte, 64), lastGranule, 0x04))
	return b.Bytes()
}

func oggPage(payload []byte, granule uint64, flags byte) []byte {
	segments := (len(payload) + 254) / 255
	p := make([]byte, 0, 27+segments+len(payload))
	p = append(p, 'O', 'g', 'g', 'S', 0, flags)
	p = binary.LittleEndian.AppendUint64(p, granule)
	p = binary.LittleEndian.AppendUint32(p, 1) // stream serial
	p = binary.LittleEndian.AppendUint32(p, 0) // page sequence
	p = binary.LittleEndian.AppendUint32(p, 0) // checksum, unread here
	p = append(p, byte(segments))
	left := len(payload)
	for range segments {
		n := min(left, 255)
		p = append(p, byte(n))
		left -= n
	}
	return append(p, payload...)
}

// mp4File writes the box tree this reader walks: moov ▸ mvhd for the duration,
// and moov ▸ trak ▸ mdia ▸ minf ▸ stbl ▸ stsd for the codec.
func mp4File(format string, rate, channels int, timescale, duration uint32) []byte {
	mvhd := make([]byte, 100)
	binary.BigEndian.PutUint32(mvhd[12:], timescale)
	binary.BigEndian.PutUint32(mvhd[16:], duration)

	entry := make([]byte, 36)
	binary.BigEndian.PutUint32(entry[0:], uint32(len(entry)))
	copy(entry[4:], format)
	binary.BigEndian.PutUint16(entry[24:], uint16(channels))
	binary.BigEndian.PutUint16(entry[26:], 16) // sample size
	binary.BigEndian.PutUint32(entry[30:], uint32(rate)<<16)

	stsd := append(make([]byte, 8), entry...)
	binary.BigEndian.PutUint32(stsd[4:], 1) // one entry

	return box("moov",
		box("mvhd", mvhd),
		box("trak", box("mdia", box("minf", box("stbl", box("stsd", stsd))))),
	)
}

func box(kind string, parts ...[]byte) []byte {
	var body []byte
	for _, p := range parts {
		body = append(body, p...)
	}
	out := make([]byte, 8, 8+len(body))
	binary.BigEndian.PutUint32(out, uint32(8+len(body)))
	copy(out[4:], kind)
	return append(out, body...)
}
