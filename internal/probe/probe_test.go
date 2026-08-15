package probe

import (
	"bytes"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// These build each container by hand rather than committing sample files.
//
// A fixture would say "this file probes to 44100 Hz"; a builder says which
// bytes make it 44100 Hz, which is the thing under test — every reader here is
// an assertion about a layout, and a layout is what a test should state. It
// also means the awkward cases (an MP3 behind a large ID3 tag, a WAV whose data
// chunk lies about its length) can be constructed instead of hunted for.
//
// The files are headers only. Nothing here decodes, so nothing here needs audio.

func write(t *testing.T, name string, b []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestMP3WithXingAndLAME(t *testing.T) {
	// 7755 frames at 1152 samples, LAME priming of 576 and 1728 trailing: the
	// shape of a real LAME-encoded file, checked against one.
	b := mp3File(t, mp3Spec{frames: 7755, bytes: 3241690, startPad: 576, endPad: 1728, cbr: true, id3: 4096})
	got, err := Inspect(write(t, "a.mp3", b))
	if err != nil {
		t.Fatal(err)
	}
	want := Info{
		Codec: "mp3", SampleRate: 44100, Channels: 2, Bitrate: 128000,
		// The pads come off: ffprobe's stream duration, which is what madshare
		// reads and therefore what a server would have stored for this file.
		DurationSeconds: float64(7755*1152-576-1728) / 44100,
		// The header frame's own 1152 samples of silence, the encoder's 576
		// priming samples, and the 529 the decoder's own pipeline owes.
		SkipSamples: 1152 + 576 + 529,
	}
	assertInfo(t, got, want)
}

// Without a Xing header there is nothing to skip and nothing to count: the
// length is the file size over the bitrate, which is what ffprobe guesses too.
func TestMP3WithoutXing(t *testing.T) {
	b := mp3File(t, mp3Spec{plainFrames: 100})
	got, err := Inspect(write(t, "a.mp3", b))
	if err != nil {
		t.Fatal(err)
	}
	if got.SkipSamples != 0 {
		t.Errorf("SkipSamples = %d, want 0: nothing declared a lead-in", got.SkipSamples)
	}
	if got.SampleRate != 44100 || got.Channels != 2 || got.Codec != "mp3" {
		t.Errorf("got %+v", got)
	}
	if want := 100 * 1152.0 / 44100; math.Abs(got.DurationSeconds-want) > 0.05 {
		t.Errorf("duration %.3f, want about %.3f", got.DurationSeconds, want)
	}
}

// A Xing frame from an encoder that is not LAME states the frame count but no
// delays. The frame is still a header, so its silence is still not audio.
func TestMP3XingWithoutLAMEStillSkipsTheHeaderFrame(t *testing.T) {
	b := mp3File(t, mp3Spec{frames: 100, bytes: 40000, noLAME: true})
	got, err := Inspect(write(t, "a.mp3", b))
	if err != nil {
		t.Fatal(err)
	}
	if got.SkipSamples != 1152 {
		t.Errorf("SkipSamples = %d, want 1152 (the header frame alone)", got.SkipSamples)
	}
}

// A mono file at a low sampling frequency puts the Xing tag at a different
// offset and halves the frame — two things that are easy to get silently wrong,
// because a wrong offset just reads as "no Xing header".
func TestMP3MonoLowSamplingFrequency(t *testing.T) {
	b := mp3File(t, mp3Spec{frames: 500, bytes: 100000, startPad: 576, mono: true, mpeg2: true})
	got, err := Inspect(write(t, "a.mp3", b))
	if err != nil {
		t.Fatal(err)
	}
	if got.Channels != 1 || got.SampleRate != 22050 {
		t.Errorf("got %d ch at %d Hz, want 1 at 22050", got.Channels, got.SampleRate)
	}
	if want := 576 + 576 + 529; got.SkipSamples != want {
		t.Errorf("SkipSamples = %d, want %d (a 576-sample frame at this version)", got.SkipSamples, want)
	}
}

func TestFLAC(t *testing.T) {
	got, err := Inspect(write(t, "a.flac", flacFile(96000, 24, 2, 4800000)))
	if err != nil {
		t.Fatal(err)
	}
	assertInfo(t, got, Info{
		Codec: "flac", SampleRate: 96000, Channels: 2, BitDepth: 24,
		DurationSeconds: 50, Bitrate: got.Bitrate,
	})
}

func TestWAV(t *testing.T) {
	got, err := Inspect(write(t, "a.wav", wavFile(44100, 16, 2, 44100*2*2)))
	if err != nil {
		t.Fatal(err)
	}
	assertInfo(t, got, Info{
		Codec: "pcm_s16le", SampleRate: 44100, Channels: 2, BitDepth: 16,
		DurationSeconds: 1, Bitrate: 44100 * 2 * 2 * 8,
	})
}

// A WAV written by a program that did not know the length in advance declares a
// data chunk running past the end of the file. The file's own size is the
// answer then — the alternative is a duration of hours for a one-second file.
func TestWAVWithAnOverlongDataChunk(t *testing.T) {
	b := wavFile(44100, 16, 2, 44100*2*2)
	binary.LittleEndian.PutUint32(b[40:], 0xFFFFFFFF)
	got, err := Inspect(write(t, "a.wav", b))
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(got.DurationSeconds-1) > 0.01 {
		t.Errorf("duration %.3f, want about 1", got.DurationSeconds)
	}
}

func TestOggOpus(t *testing.T) {
	// Opus counts its granule at 48 kHz whatever it decodes to, so 96000 is two
	// seconds even though the header says the source was 44100.
	got, err := Inspect(write(t, "a.opus", oggFile(opusHead(2, 44100), 96000)))
	if err != nil {
		t.Fatal(err)
	}
	assertInfo(t, got, Info{
		Codec: "opus", SampleRate: 44100, Channels: 2,
		DurationSeconds: 2, Bitrate: got.Bitrate,
	})
}

func TestOggVorbis(t *testing.T) {
	got, err := Inspect(write(t, "a.ogg", oggFile(vorbisHead(2, 44100), 88200)))
	if err != nil {
		t.Fatal(err)
	}
	assertInfo(t, got, Info{
		Codec: "vorbis", SampleRate: 44100, Channels: 2,
		DurationSeconds: 2, Bitrate: got.Bitrate,
	})
}

func TestM4A(t *testing.T) {
	got, err := Inspect(write(t, "a.m4a", mp4File("mp4a", 44100, 2, 44100, 44100*3)))
	if err != nil {
		t.Fatal(err)
	}
	assertInfo(t, got, Info{
		Codec: "aac", SampleRate: 44100, Channels: 2,
		DurationSeconds: 3, Bitrate: got.Bitrate,
	})
}

// ALAC is lossless, so unlike AAC it has a sample depth worth reporting.
func TestM4AWithALAC(t *testing.T) {
	got, err := Inspect(write(t, "a.m4a", mp4File("alac", 44100, 2, 1000, 2000)))
	if err != nil {
		t.Fatal(err)
	}
	if got.Codec != "alac" || got.DurationSeconds != 2 {
		t.Errorf("got %+v", got)
	}
}

func TestUnknownExtension(t *testing.T) {
	if _, err := Inspect(write(t, "a.xyz", []byte("nope"))); err == nil {
		t.Error("probed a format it has no reader for")
	}
}

func TestNotAudio(t *testing.T) {
	if _, err := Inspect(write(t, "a.mp3", bytes.Repeat([]byte{0x37}, 4096))); err == nil {
		t.Error("found an MP3 frame in a file that has none")
	}
}

func assertInfo(t *testing.T, got *Info, want Info) {
	t.Helper()
	if got.Codec != want.Codec {
		t.Errorf("Codec = %q, want %q", got.Codec, want.Codec)
	}
	if got.SampleRate != want.SampleRate {
		t.Errorf("SampleRate = %d, want %d", got.SampleRate, want.SampleRate)
	}
	if got.Channels != want.Channels {
		t.Errorf("Channels = %d, want %d", got.Channels, want.Channels)
	}
	if got.BitDepth != want.BitDepth {
		t.Errorf("BitDepth = %d, want %d", got.BitDepth, want.BitDepth)
	}
	if math.Abs(got.DurationSeconds-want.DurationSeconds) > 0.001 {
		t.Errorf("DurationSeconds = %v, want %v", got.DurationSeconds, want.DurationSeconds)
	}
	if want.Bitrate != 0 && got.Bitrate != want.Bitrate {
		t.Errorf("Bitrate = %d, want %d", got.Bitrate, want.Bitrate)
	}
	if got.SkipSamples != want.SkipSamples {
		t.Errorf("SkipSamples = %d, want %d", got.SkipSamples, want.SkipSamples)
	}
}
