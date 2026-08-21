package analyze_test

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"daemonlord.ygg/madplayer/internal/analyze"
)

// A file whose header lies must not take the program with it.
//
// beep's WAV decoder computes a frame size from the format chunk and indexes its
// buffer by it, so a header claiming a zero-byte frame panics on the first
// sample. Fingerprinting runs on a worker pool with no recover of its own, which
// makes that panic a process exit — from one damaged file in a scanned folder,
// or a download that was cut short.
func TestADamagedFileIsAnErrorAndNotAPanic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "damaged.wav")

	var body []byte
	chunk := func(id string, b []byte) {
		body = append(body, id...)
		var n [4]byte
		binary.LittleEndian.PutUint32(n[:], uint32(len(b)))
		body = append(body, n[:]...)
		body = append(body, b...)
	}
	// A format chunk that says PCM, two channels, 24 bits — and leaves the block
	// alignment at zero, which is the lie.
	f := make([]byte, 16)
	binary.LittleEndian.PutUint16(f[0:], 1)
	binary.LittleEndian.PutUint16(f[2:], 2)
	binary.LittleEndian.PutUint32(f[4:], 48000)
	binary.LittleEndian.PutUint16(f[14:], 24)
	chunk("fmt ", f)
	chunk("data", make([]byte, 4096))

	out := append([]byte("RIFF"), 0, 0, 0, 0)
	binary.LittleEndian.PutUint32(out[4:], uint32(4+len(body)))
	out = append(out, "WAVE"...)
	out = append(out, body...)
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatal(err)
	}

	raw, _, err := analyze.Fingerprint(context.Background(), path)
	if err == nil {
		t.Fatalf("a damaged WAV fingerprinted to %d words, want an error", len(raw))
	}
	if !strings.Contains(err.Error(), "damaged.wav") {
		t.Errorf("error = %v, want it to name the file", err)
	}
}
