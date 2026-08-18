package logbuf

import (
	"strconv"
	"strings"
	"testing"
)

func reset() {
	mu.Lock()
	ring, next, wrapped, dropped = nil, 0, false, 0
	mu.Unlock()
}

func TestLinesAreStampedAndSplit(t *testing.T) {
	reset()
	add("one\ntwo\n")
	got := Snapshot()
	if len(got) != 2 {
		t.Fatalf("Snapshot() = %d lines, want 2", len(got))
	}
	for i, want := range []string{"one", "two"} {
		if !strings.HasSuffix(got[i], " "+want) {
			t.Errorf("line %d = %q, want suffix %q", i, got[i], want)
		}
		// "15:04:05.000 " is 13 bytes of stamp.
		if len(got[i]) != 13+len(want) {
			t.Errorf("line %d = %q, want a 13-byte millisecond stamp", i, got[i])
		}
	}
	if Count() != 2 {
		t.Errorf("Count() = %d, want 2", Count())
	}
}

func TestWrapKeepsTheNewestAndSaysWhatItDropped(t *testing.T) {
	reset()
	for i := 0; i < maxLines+7; i++ {
		add(strings.Repeat("x", 3) + "-" + strconv.Itoa(i) + "\n")
	}
	got := Snapshot()
	if len(got) != maxLines+1 {
		t.Fatalf("Snapshot() = %d lines, want %d + the drop notice", len(got), maxLines)
	}
	if !strings.Contains(got[0], "7 earlier lines dropped") {
		t.Errorf("first line = %q, want the drop notice", got[0])
	}
	if want := "xxx-" + strconv.Itoa(7); !strings.HasSuffix(got[1], want) {
		t.Errorf("oldest kept line = %q, want suffix %q", got[1], want)
	}
	if want := "xxx-" + strconv.Itoa(maxLines + 6); !strings.HasSuffix(got[len(got)-1], want) {
		t.Errorf("newest line = %q, want suffix %q", got[len(got)-1], want)
	}
	if Count() != len(got) {
		t.Errorf("Count() = %d, Snapshot has %d", Count(), len(got))
	}
}

func TestTeeStillFeedsThePreviousWriter(t *testing.T) {
	reset()
	var prev strings.Builder
	w := tee{prev: &prev}
	if _, err := w.Write([]byte("hello\n")); err != nil {
		t.Fatal(err)
	}
	if prev.String() != "hello\n" {
		t.Errorf("previous writer got %q, want the write untouched", prev.String())
	}
	if got := Snapshot(); len(got) != 1 || !strings.HasSuffix(got[0], " hello") {
		t.Errorf("ring got %v, want the same line", got)
	}
}

