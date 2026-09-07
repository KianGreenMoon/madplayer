package ui

import (
	"strings"
	"testing"
	"time"
)

// The key acts on a player with no mesh — the state testApp builds, and the
// one where a nil is most likely to be missing a check — must answer in
// words, not panic: there is no node and so no key.
func TestKeyActsWithoutANodeSaySo(t *testing.T) {
	a := testApp(t)

	a.backUpKey("")
	if msg := a.pairMsg(); !strings.Contains(msg, "where to keep") {
		t.Fatalf("an empty path should ask for one, got %q", msg)
	}
	a.restoreKey("")
	if msg := a.pairMsg(); !strings.Contains(msg, "path of the key") {
		t.Fatalf("an empty path should ask for one, got %q", msg)
	}

	a.backUpKey(t.TempDir())
	waitPairMsg(t, a, "no node")
	a.restoreKey(t.TempDir() + "/nothing.key")
	waitPairMsg(t, a, "no node")

	if a.be.PendingKey() != "" {
		t.Fatalf("no restore happened, yet a key is pending")
	}
	// The path box is in the typing gate: TestEveryEditorIsInTheTypingGate
	// walks the struct, so this only pins that the field exists by that name.
	if !a.keyPathEd.SingleLine {
		t.Fatalf("keyPathEd is a path box; it must be single-line")
	}
}

func (a *App) pairMsg() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.pairing.msg
}

// waitPairMsg waits for the act's goroutine to report.
func waitPairMsg(t *testing.T, a *App, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(a.pairMsg(), want) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("message never said %q, last: %q", want, a.pairMsg())
}
