package probe

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// MP3, the only format here whose header is a search rather than a read.
//
// There is no container: a file is a run of frames, possibly behind an ID3v2
// tag, possibly behind junk. The first frame usually is not audio at all but a
// Xing/Info header carrying the frame count and the encoder's delays, and the
// facts worth having are all in it.

// mp3MaxScan bounds the hunt for the first frame. Past this the file is not
// something a frame sync is going to rescue.
const mp3MaxScan = 1 << 16

// bitrates are kbit/s by (version group, layer, index). Index 0 is "free" and
// 15 is invalid; both read as 0 and reject the candidate frame.
var mp3Bitrates = map[[2]int][16]int{
	// MPEG 1
	{1, 1}: {0, 32, 64, 96, 128, 160, 192, 224, 256, 288, 320, 352, 384, 416, 448},
	{1, 2}: {0, 32, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 384},
	{1, 3}: {0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320},
	// MPEG 2 and 2.5
	{2, 1}: {0, 32, 48, 56, 64, 80, 96, 112, 128, 144, 160, 176, 192, 224, 256},
	{2, 2}: {0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160},
	{2, 3}: {0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160},
}

var mp3Rates = map[int][3]int{
	3: {44100, 48000, 32000}, // MPEG 1
	2: {22050, 24000, 16000}, // MPEG 2
	0: {11025, 12000, 8000},  // MPEG 2.5
}

// mp3Frame is one decoded frame header.
type mp3Frame struct {
	version    int // 3 = MPEG1, 2 = MPEG2, 0 = MPEG2.5
	layer      int // 1, 2 or 3
	bitrate    int // bits per second, 0 for "free"
	sampleRate int
	channels   int
	size       int // bytes, including the header
	samples    int // per frame
}

func inspectMP3(r io.ReadSeeker, size int64) (*Info, error) {
	head := make([]byte, mp3MaxScan)
	n, err := io.ReadFull(r, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return nil, err
	}
	head = head[:n]

	start := skipID3v2(head)
	off, frame, ok := findFrame(head, start)
	if !ok {
		return nil, errors.New("probe: no MP3 frame found")
	}

	info := &Info{
		Codec:      fmt.Sprintf("mp%d", frame.layer),
		SampleRate: frame.sampleRate,
		Channels:   frame.channels,
		Bitrate:    frame.bitrate,
	}
	// Audio bytes exclude whatever preceded the first frame — an ID3v2 tag can
	// be a megabyte of cover art, and counting it as audio inflates the bitrate
	// of exactly the files people have art on.
	audioBytes := size - int64(off)

	x, tagged := parseXing(head[off:], frame)
	if tagged {
		info.SkipSamples = x.skip
		if x.frames > 0 {
			// The frame count is authoritative where it exists, and the pads come
			// off it: they are audio in the file that was never in the recording.
			//
			// This is the STREAM duration, and ffprobe prints two. Its format
			// duration keeps the pads (and is 52 ms longer on a typical LAME
			// file); madshare reads the stream's and only falls back to the
			// format's, so the stream's is the number that has to match. Checked
			// against ffprobe 7.1 rather than reasoned about — the first attempt
			// here matched the other one.
			samples := int64(x.frames)*int64(frame.samples) - int64(x.startPad) - int64(x.endPad)
			if samples > 0 {
				info.DurationSeconds = float64(samples) / float64(frame.sampleRate)
			}
		}
		if x.bytes > 0 {
			audioBytes = int64(x.bytes)
		}
	}
	if info.DurationSeconds == 0 && frame.bitrate > 0 {
		// Nothing declared: assume the first frame's rate holds throughout, which
		// is right for CBR and is the same guess ffprobe makes.
		info.DurationSeconds = float64(audioBytes-int64(frame.size)) * 8 / float64(frame.bitrate)
	}
	// A CBR file keeps the rate its frames state; only a VBR one has to be
	// averaged, and ffmpeg averages it over the size the header declares rather
	// than over the file (which would count the tags). is_cbr is the Info
	// spelling of the tag, as against Xing.
	if !x.cbr && x.bytes > 0 && info.DurationSeconds > 0 {
		info.Bitrate = bitrateFrom(int64(x.bytes), info.DurationSeconds)
	}
	return info, nil
}

// skipID3v2 returns the offset past a leading ID3v2 tag, or 0.
func skipID3v2(b []byte) int {
	if len(b) < 10 || string(b[:3]) != "ID3" {
		return 0
	}
	// A syncsafe integer: seven bits per byte, so no byte can look like a frame
	// sync.
	n := int(b[6])<<21 | int(b[7])<<14 | int(b[8])<<7 | int(b[9])
	if b[5]&0x10 != 0 {
		n += 10 // a footer, present only if the flag says so
	}
	if off := n + 10; off < len(b) {
		return off
	}
	return 0
}

// findFrame scans forward for a frame header that is followed by another one
// where it says the next should be. One valid-looking header is common in
// binary data; two in a row at the right distance is not.
func findFrame(b []byte, from int) (int, mp3Frame, bool) {
	for i := from; i+4 <= len(b); i++ {
		f, ok := parseFrameHeader(b[i:])
		if !ok {
			continue
		}
		next := i + f.size
		if next+4 <= len(b) {
			if _, ok := parseFrameHeader(b[next:]); !ok {
				continue
			}
		}
		return i, f, true
	}
	return 0, mp3Frame{}, false
}

// parseFrameHeader decodes the four-byte header at the front of b.
func parseFrameHeader(b []byte) (mp3Frame, bool) {
	if len(b) < 4 || b[0] != 0xFF || b[1]&0xE0 != 0xE0 {
		return mp3Frame{}, false
	}
	var f mp3Frame
	f.version = int(b[1] >> 3 & 0x03) // 3=MPEG1, 2=MPEG2, 0=MPEG2.5, 1 reserved
	layerBits := int(b[1] >> 1 & 0x03)
	if f.version == 1 || layerBits == 0 {
		return mp3Frame{}, false
	}
	f.layer = 4 - layerBits

	group := 2
	if f.version == 3 {
		group = 1
	}
	table, ok := mp3Bitrates[[2]int{group, f.layer}]
	if !ok {
		return mp3Frame{}, false
	}
	kbps := table[b[2]>>4]
	rateIdx := int(b[2] >> 2 & 0x03)
	if kbps == 0 || rateIdx == 3 {
		return mp3Frame{}, false
	}
	f.bitrate = kbps * 1000
	f.sampleRate = mp3Rates[f.version][rateIdx]

	f.channels = 2
	if b[3]>>6&0x03 == 3 {
		f.channels = 1
	}

	switch {
	case f.layer == 1:
		f.samples = 384
	case f.layer == 2 || f.version == 3:
		f.samples = 1152
	default:
		// Layer 3 at the low sampling frequencies halves the frame.
		f.samples = 576
	}

	pad := int(b[2] >> 1 & 0x01)
	if f.layer == 1 {
		f.size = (12*f.bitrate/f.sampleRate + pad) * 4
	} else {
		f.size = f.samples/8*f.bitrate/f.sampleRate + pad
	}
	if f.size < 4 {
		return mp3Frame{}, false
	}
	return f, true
}

// xing is what a Xing/Info header frame declares.
type xing struct {
	frames   int
	bytes    int
	startPad int
	endPad   int
	cbr      bool // the tag reads "Info", the spelling reserved for constant rate

	// skip is the leading samples a correct decoder never emits: the header
	// frame's own silence plus the encoder's priming, plus the 529 samples of
	// the decoder's own pipeline delay. ffmpeg's mp3 demuxer computes the last
	// two as start_pad + 528 + 1 (libavformat/mp3dec.c) and drops the header
	// frame by never emitting it as a packet.
	skip int
}

// parseXing reads the Xing/Info header out of the first frame, if it is one.
func parseXing(frameBytes []byte, f mp3Frame) (xing, bool) {
	// The tag sits at a fixed offset that depends only on whether this is a low
	// sampling frequency version and whether it is mono — it lives where the
	// side info would be, and the side info is that long.
	offsets := [2][2]int{{32, 17}, {17, 9}}
	lsf, mono := 0, 0
	if f.version != 3 {
		lsf = 1
	}
	if f.channels == 1 {
		mono = 1
	}
	p := 4 + offsets[lsf][mono]
	if len(frameBytes) < p+8 {
		return xing{}, false
	}
	tag := string(frameBytes[p : p+4])
	if tag != "Xing" && tag != "Info" {
		return xing{}, false
	}
	flags := binary.BigEndian.Uint32(frameBytes[p+4:])
	p += 8

	// This frame is a header. Whether or not it also carries an encoder's
	// delays, its own samples are silence a decoder must not emit — so the skip
	// starts at a whole frame and grows if the LAME extension says more.
	x := xing{skip: f.samples, cbr: tag == "Info"}
	read4 := func() (uint32, bool) {
		if len(frameBytes) < p+4 {
			return 0, false
		}
		v := binary.BigEndian.Uint32(frameBytes[p:])
		p += 4
		return v, true
	}
	if flags&0x1 != 0 {
		v, ok := read4()
		if !ok {
			return x, true
		}
		x.frames = int(v)
	}
	if flags&0x2 != 0 {
		v, ok := read4()
		if !ok {
			return x, true
		}
		x.bytes = int(v)
	}
	if flags&0x4 != 0 {
		p += 100 // the seek table, which nothing here needs
	}
	if flags&0x8 != 0 {
		p += 4 // VBR quality
	}

	// The LAME extension follows, and its delay field is only trustworthy from
	// encoders known to write it the way the field is defined.
	if len(frameBytes) < p+9+12+3 {
		return x, true
	}
	switch string(frameBytes[p : p+4]) {
	case "LAME", "Lavf", "Lavc":
	default:
		return x, true
	}
	d := frameBytes[p+21:]
	v := uint32(d[0])<<16 | uint32(d[1])<<8 | uint32(d[2])
	x.startPad = int(v >> 12)
	x.endPad = int(v & 0xFFF)
	x.skip += x.startPad + 529
	return x, true
}
