package ui

import (
	"testing"

	"gioui.org/widget"

	"daemonlord.ygg/madplayer/internal/backend"
)

// The editor under a peer row: opened for one act on one row, prefilled for
// a rename and empty for a block, moved by a press elsewhere, closed by
// cancel — and a commit with nothing open is a no-op.
func TestPeerEditOpensMovesAndCloses(t *testing.T) {
	a := testApp(t)
	one := backend.Peer{ID: 7, Name: "the laptop", State: "friend"}
	two := backend.Peer{ID: 9, State: "pending_outgoing"}

	a.beginPeerEdit(one, "rename")
	if a.pairing.editing != 7 || a.pairing.editKind != "rename" || a.peerEditEd.Text() != "the laptop" {
		t.Fatalf("rename should open on row 7 prefilled: %d %q %q", a.pairing.editing, a.pairing.editKind, a.peerEditEd.Text())
	}
	a.beginPeerEdit(two, "block")
	if a.pairing.editing != 9 || a.pairing.editKind != "block" || a.peerEditEd.Text() != "" {
		t.Fatalf("block should move the editor to row 9, empty: %d %q %q", a.pairing.editing, a.pairing.editKind, a.peerEditEd.Text())
	}
	a.cancelPeerEdit()
	if a.pairing.editing != 0 || a.pairing.editKind != "" {
		t.Fatalf("cancel left the editor open")
	}
	a.commitPeerEdit() // nothing open: must not act or panic
	if !a.peerEditEd.SingleLine {
		t.Fatal("the edit box is a line, not a page")
	}
	// A blocked row lays out with its Unblock in the block slot.
	a.pairing.accept = append(a.pairing.accept, make([]widget.Clickable, 1)...)
	a.pairing.remove = append(a.pairing.remove, make([]widget.Clickable, 1)...)
	a.pairing.rename = append(a.pairing.rename, make([]widget.Clickable, 1)...)
	a.pairing.block = append(a.pairing.block, make([]widget.Clickable, 1)...)
	a.pairing.unblock = append(a.pairing.unblock, make([]widget.Clickable, 1)...)
	if d := a.peerRow(0, backend.Peer{ID: 1, State: "blocked", Key: "abcd"}, false)(headless()); d.Size.Y == 0 {
		t.Fatal("a blocked row laid out to nothing")
	}
	a.beginPeerEdit(one, "block")
	if d := a.peerEditRow(one, false)(headless()); d.Size.Y == 0 {
		t.Fatal("the editor row laid out to nothing")
	}
}
