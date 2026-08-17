package mediasession

// modUTF8 encodes a Go string as the "modified UTF-8" JNI's NewStringUTF
// demands, NUL-terminated.
//
// This is not pedantry — it is a crash. Real UTF-8 and modified UTF-8 agree on
// everything a Latin-alphabet library contains, then diverge exactly where
// music metadata gets interesting: a title with an emoji or any other
// character beyond the BMP is a four-byte sequence in Go, and Android's JNI
// under CheckJNI ABORTS THE PROCESS when NewStringUTF receives one ("input is
// not valid Modified UTF-8"). Modified UTF-8 wants the surrogate pair, each
// half encoded as three bytes (CESU-8), and an embedded NUL as 0xC0 0x80.
//
// It lives outside the android build tag so the desktop can test it, which is
// the only place a test runs before the phone does.
func modUTF8(s string) []byte {
	b := make([]byte, 0, len(s)+1)
	for _, r := range s {
		switch {
		case r == 0:
			b = append(b, 0xC0, 0x80)
		case r < 0x80:
			b = append(b, byte(r))
		case r < 0x800:
			b = append(b, 0xC0|byte(r>>6), 0x80|byte(r&0x3F))
		case r < 0x10000:
			// A lone surrogate cannot appear here: Go's range over a string
			// yields U+FFFD for invalid bytes, never D800–DFFF.
			b = append(b, 0xE0|byte(r>>12), 0x80|byte((r>>6)&0x3F), 0x80|byte(r&0x3F))
		default:
			v := r - 0x10000
			for _, half := range [2]rune{0xD800 + (v >> 10), 0xDC00 + (v & 0x3FF)} {
				b = append(b, 0xE0|byte(half>>12), 0x80|byte((half>>6)&0x3F), 0x80|byte(half&0x3F))
			}
		}
	}
	return append(b, 0)
}
