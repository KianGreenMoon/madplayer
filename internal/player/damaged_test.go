package player

import (
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gopxl/beep/v2"
)

// A file that is damaged, or one that lies, must not end the program.
//
// The decoders trust their own headers, and beep's WAV decoder is the plain
// case: it derives a frame size from the format chunk and then indexes its read
// buffer by it, so a header claiming a zero-byte frame panics on the first
// sample and divides by zero when asked its length. Playback runs that decoder
// on the decode-ahead goroutine and inside the audio device's own pull, where a
// panic is not a bad track — it is the process.

// damagedWAV writes a RIFF/WAVE file whose format chunk leaves the block
// alignment at zero. Everything else about it is ordinary.
func damagedWAV(t *testing.T) string {
	t.Helper()
	var body []byte
	chunk := func(id string, b []byte) {
		body = append(body, id...)
		var n [4]byte
		binary.LittleEndian.PutUint32(n[:], uint32(len(b)))
		body = append(body, n[:]...)
		body = append(body, b...)
	}
	f := make([]byte, 16)
	binary.LittleEndian.PutUint16(f[0:], 1)     // PCM
	binary.LittleEndian.PutUint16(f[2:], 2)     // channels
	binary.LittleEndian.PutUint32(f[4:], 44100) // sample rate
	binary.LittleEndian.PutUint16(f[14:], 16)   // bits — and no block align
	chunk("fmt ", f)
	chunk("data", make([]byte, 8192))

	out := append([]byte("RIFF"), 0, 0, 0, 0)
	binary.LittleEndian.PutUint32(out[4:], uint32(4+len(body)))
	out = append(out, "WAVE"...)
	out = append(out, body...)

	path := filepath.Join(t.TempDir(), "damaged.wav")
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// Decoding it is an error with a reason, not a crash — and the reason names the
// decoder, because "this track will not play" without one is a bug report
// nobody can act on.
func TestADamagedFileEndsTheTrackAndNotTheProgram(t *testing.T) {
	src, err := open(damagedWAV(t))
	if err != nil {
		// A decoder that refuses the file outright is a fine outcome too; what
		// must not happen is the panic, and either way we are past it.
		return
	}
	defer src.Close()

	buf := make([][2]float64, 512)
	n, ok := src.streamer.Stream(buf)
	if ok || n != 0 {
		t.Fatalf("Stream = (%d, %v) on a damaged file, want the end of the stream", n, ok)
	}
	err = src.streamer.Err()
	if err == nil {
		t.Fatal("the stream ended with no reason given")
	}
	if !strings.Contains(err.Error(), "wav") {
		t.Errorf("error = %v, want it to name the decoder", err)
	}

	// Len divides by the same lie. Probe asks for it on every track whose
	// duration the library does not know.
	if got := src.streamer.Len(); got != 0 {
		t.Errorf("Len = %d on a damaged file, want 0", got)
	}
	if _, err := Probe(damagedWAV(t)); err == nil {
		t.Error("Probe reported a duration for a damaged file")
	}
}

// panicking is a decoder that dies partway through a track, which is what a file
// truncated mid-download looks like to a parser that has already read a good
// header.
type panicking struct {
	calls int
	// after is how many good calls come first, so the test covers a failure in
	// the middle of playback rather than only at the start.
	after int
}

func (p *panicking) Stream(samples [][2]float64) (int, bool) {
	p.calls++
	if p.calls > p.after {
		panic("decoder read past the end of its buffer")
	}
	for i := range samples {
		samples[i] = [2]float64{0.25, 0.25}
	}
	return len(samples), true
}

func (p *panicking) Err() error     { return nil }
func (p *panicking) Len() int       { return 44100 * 10 }
func (p *panicking) Position() int  { return p.calls * 512 }
func (p *panicking) Seek(int) error { panic("seek into a damaged file") }
func (p *panicking) Close() error   { return nil }

// The guard is on every call, not only the first one: a decoder that dies in the
// middle of a track is the ordinary shape of a truncated download.
func TestADecoderThatDiesMidTrackIsAnError(t *testing.T) {
	g := guard(&panicking{after: 2}, ".flac")

	buf := make([][2]float64, 512)
	for i := 0; i < 2; i++ {
		if n, ok := g.Stream(buf); !ok || n != len(buf) {
			t.Fatalf("call %d = (%d, %v), want a full buffer", i, n, ok)
		}
	}
	n, ok := g.Stream(buf)
	if ok || n != 0 {
		t.Fatalf("the failing call returned (%d, %v), want the end of the stream", n, ok)
	}
	err := g.Err()
	if err == nil || !strings.Contains(err.Error(), "flac") {
		t.Fatalf("Err = %v, want a reason naming the decoder", err)
	}
	// The first reason is the one kept: a decoder that has already panicked is
	// in an unknown state and its later complaints explain less.
	g.Stream(buf)
	if g.Err().Error() != err.Error() {
		t.Errorf("the reason changed after a second failure: %v", g.Err())
	}
	// Seek is guarded too, and reports the same failure rather than dying.
	if err := g.Seek(0); err == nil {
		t.Error("Seek on a damaged decoder reported success")
	}
}

// The decode-ahead goroutine carries the same protection, one layer out: it is
// a goroutine this package started, and an unowned panic on it takes the whole
// program with it however the track got into that state.
func TestTheDecodeAheadGoroutineSurvivesAPanic(t *testing.T) {
	// Deliberately NOT wrapped in guard: this is the layer under it, and a test
	// that leant on the wrapper would pass with the goroutine's own defence
	// removed.
	b := newBuffered(&panicking{after: 1}, beep.SampleRate(44100), beep.SampleRate(44100), false)
	defer b.Close()

	deadline := time.After(5 * time.Second)
	for {
		buf := make([][2]float64, 512)
		if _, ok := b.Stream(buf); !ok {
			break
		}
		select {
		case <-deadline:
			t.Fatal("the ring never ended the track")
		default:
		}
	}
	if err := b.Err(); err == nil {
		t.Fatal("the track ended with no reason given")
	}
	select {
	case <-b.exited:
	case <-time.After(5 * time.Second):
		t.Fatal("the fill goroutine did not finish")
	}
}

// A reader whose bytes are not audio at all must be refused rather than
// survived: the guard around the decoder's CONSTRUCTION is the one that
// otherwise leaves a half-built parser to explode on the first sample.
func TestGarbageIsRefusedAtTheDoor(t *testing.T) {
	rc := io.NopCloser(strings.NewReader(strings.Repeat("not audio", 200)))
	src, err := openReader(readCloser{rc}, ".flac")
	if err == nil {
		src.Close()
		t.Fatal("garbage decoded successfully")
	}
	if errors.Is(err, io.EOF) {
		t.Errorf("error = %v, want something a person can read", err)
	}
}

// readCloser gives a reader the Close the decoder path expects, without making
// it seekable — the shape a streaming source has.
type readCloser struct{ io.ReadCloser }

// A header claiming no sample rate is refused at the door for the same reason a
// panic is caught: everything downstream divides by it. The resampler builds its
// phase table from the ratio of the two rates, and a zero there is not an error
// it can report — it is undefined behaviour in the audio path.
func TestAFileWithNoSampleRateIsRefused(t *testing.T) {
	var body []byte
	chunk := func(id string, b []byte) {
		body = append(body, id...)
		var n [4]byte
		binary.LittleEndian.PutUint32(n[:], uint32(len(b)))
		body = append(body, n[:]...)
		body = append(body, b...)
	}
	f := make([]byte, 16)
	binary.LittleEndian.PutUint16(f[0:], 1)   // PCM
	binary.LittleEndian.PutUint16(f[2:], 2)   // channels
	binary.LittleEndian.PutUint32(f[4:], 0)   // sample rate: none
	binary.LittleEndian.PutUint16(f[12:], 4)  // block align
	binary.LittleEndian.PutUint16(f[14:], 16) // bits
	chunk("fmt ", f)
	chunk("data", make([]byte, 4096))

	out := append([]byte("RIFF"), 0, 0, 0, 0)
	binary.LittleEndian.PutUint32(out[4:], uint32(4+len(body)))
	out = append(out, "WAVE"...)
	out = append(out, body...)

	path := filepath.Join(t.TempDir(), "norate.wav")
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatal(err)
	}
	src, err := open(path)
	if err == nil {
		src.Close()
		t.Fatal("a file claiming 0 Hz opened for playback")
	}
	if !strings.Contains(err.Error(), "sample rate") {
		t.Errorf("error = %v, want it to name the reason", err)
	}
}
