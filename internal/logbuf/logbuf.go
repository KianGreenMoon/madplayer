// Package logbuf keeps the tail of this program's own log in memory.
//
// It exists because of where the log goes on a phone. On Android, Gio routes
// log output to logcat — and on the Pixel 7 Pro that diagnosed the 2026-08-18
// crackle, logcat's main ring is 512 KiB and holds about ten minutes of a
// busy system. The evidence a crackle report needs (audio: feed starved …)
// was gone hours before anybody could plug in a cable. This ring is the copy
// the program keeps for itself: the Debugging page in Settings shows it, and
// its Copy button is how the lines leave a phone that has no adb around.
//
// It is a tee, not a replacement — every line still reaches wherever the log
// already went (logcat on Android, stderr on a desktop).
package logbuf

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
)

// maxLines bounds the ring. At the sizes this program logs, two thousand
// lines is hours of playback including the periodic audio stats — and far
// beyond what logcat kept on the phone that motivated this package.
const maxLines = 2000

var (
	mu      sync.Mutex
	ring    []string
	next    int // where the oldest line is once the ring has wrapped
	wrapped bool
	dropped int
)

// Install tees the standard logger into the ring. Call it once, early in
// main, and AFTER any platform init that redirects the log (Gio's Android
// init replaces the writer outright and would silence the ring if it ran
// second — package inits all run before main, so calling from main is safe).
func Install() {
	log.SetOutput(tee{prev: log.Writer()})
}

type tee struct{ prev interface{ Write([]byte) (int, error) } }

func (t tee) Write(p []byte) (int, error) {
	if t.prev != nil {
		t.prev.Write(p) // the ring must never eat the real log
	}
	add(string(p))
	return len(p), nil
}

// add stamps and stores each line of one log write. The stamp is the ring's
// own, with milliseconds: audio problems live at millisecond scale, and the
// standard logger's second-resolution stamp (absent entirely on Android,
// where logcat was expected to provide one) is not enough to line a starved
// read up against a GC pause.
func add(msg string) {
	stamp := time.Now().Format("15:04:05.000 ")
	mu.Lock()
	defer mu.Unlock()
	for _, line := range strings.Split(msg, "\n") {
		if line == "" {
			continue
		}
		if len(ring) < maxLines {
			ring = append(ring, stamp+line)
			continue
		}
		ring[next] = stamp + line
		next = (next + 1) % maxLines
		wrapped = true
		dropped++
	}
}

// Count is the number of lines a Snapshot would return, cheap enough for a
// layout pass that runs sixty times a second.
func Count() int {
	mu.Lock()
	defer mu.Unlock()
	n := len(ring)
	if dropped > 0 {
		n++ // the dropped-lines notice Snapshot prepends
	}
	return n
}

// Snapshot is the ring's contents, oldest first. Once lines have been pushed
// out, the first entry says how many — a log that silently starts in the
// middle reads as if nothing happened before it.
func Snapshot() []string {
	mu.Lock()
	defer mu.Unlock()
	out := make([]string, 0, len(ring)+1)
	if dropped > 0 {
		out = append(out, fmt.Sprintf("… %d earlier lines dropped (the ring keeps the last %d)", dropped, maxLines))
	}
	if wrapped {
		out = append(out, ring[next:]...)
		out = append(out, ring[:next]...)
		return out
	}
	return append(out, ring...)
}
