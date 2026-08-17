package ui

import (
	"io"
	"reflect"
	"strings"
	"testing"

	"gioui.org/io/input"
	"gioui.org/io/transfer"
	"gioui.org/widget"
)

// The clipboard buttons are the only way to get text into or out of a settings
// box on a phone, and no button on this host can be clicked (CLAUDE.md
// §Gotchas). So the tests drive a real input router instead, which is what makes
// a programmatic click and a delivered paste take exactly the path a tap does:
// the button asks the SYSTEM for the clipboard, the answer comes back a frame or
// more later as a transfer event, and only then does it reach the box.
//
// Laying the row out against a router rather than calling clipUpdate directly is
// the point — a filter registered in a frame nobody lays out is a paste that
// never arrives, which is the failure this whole file is about.

// clipRig is one text box, its buttons, and a router to run frames against.
type clipRig struct {
	a  *App
	r  input.Router
	cb clipButtons
	ed widget.Editor
}

func newClipRig(t *testing.T) *clipRig {
	t.Helper()
	return &clipRig{a: testApp(t)}
}

// frame lays the row out and lets the router process what it asked for.
func (g *clipRig) frame(canCopy bool) {
	gtx := headless()
	gtx.Source = g.r.Source()
	g.a.clipRow(gtx, &g.cb, &g.ed, "hint", canCopy)
	g.r.Frame(gtx.Ops)
}

// paste hands the box what the system would, the way the platform layer does.
func (g *clipRig) paste(text string) {
	g.r.Queue(transfer.DataEvent{
		Type: clipMIME,
		Open: func() io.ReadCloser { return io.NopCloser(strings.NewReader(text)) },
	})
}

// A settings box holds one whole value, so a paste replaces it rather than
// inserting at the caret — and arrives trimmed, because the newline that comes
// with a path copied off a web page is not part of the path.
func TestPastingReplacesTheWholeBoxAndTrimsIt(t *testing.T) {
	g := newClipRig(t)
	g.ed.SetText("/old/music")

	g.cb.paste.Click()
	g.frame(true)
	if !g.r.ClipboardRequested() {
		t.Fatal("the paste button asked the system for nothing — no tap on a phone can reach this box")
	}

	g.paste("  /home/you/Musik\n")
	g.frame(true)

	if got := g.ed.Text(); got != "/home/you/Musik" {
		t.Errorf("the box holds %q, want the pasted path alone", got)
	}
}

// Nothing to paste must leave the box alone: emptying it would look exactly like
// a paste that worked on a value that happened to be blank.
func TestPastingNothingLeavesTheBoxAlone(t *testing.T) {
	g := newClipRig(t)
	g.ed.SetText("/home/you/Musik")

	g.cb.paste.Click()
	g.frame(true)
	g.paste("   \n")
	g.frame(true)

	if got := g.ed.Text(); got != "/home/you/Musik" {
		t.Errorf("an empty clipboard changed the box to %q", got)
	}
}

// Copy is the other half a phone has no shortcut for. It hands over the whole
// field — there is no selection to speak of on a touchscreen.
func TestCopyingHandsTheWholeFieldToTheSystem(t *testing.T) {
	g := newClipRig(t)
	g.ed.SetText("music.example:3000")

	g.cb.copy.Click()
	g.frame(true)

	mime, content, ok := g.r.WriteClipboard()
	if !ok {
		t.Fatal("the copy button put nothing on the clipboard")
	}
	if mime != clipMIME {
		t.Errorf("copied as %q, which is not the type Gio's own paste accepts", mime)
	}
	if string(content) != "music.example:3000" {
		t.Errorf("copied %q", content)
	}
}

// An empty box must not reach the clipboard at all. Overwriting whatever was
// there with nothing is the opposite of what the button offers, and it is the
// one clipboard mistake that destroys something outside this program.
func TestCopyingAnEmptyBoxDoesNotWipeTheClipboard(t *testing.T) {
	g := newClipRig(t)
	g.ed.SetText("")

	g.cb.copy.Click()
	g.frame(true)

	if mime, content, ok := g.r.WriteClipboard(); ok {
		t.Errorf("an empty box wrote %q (%s) over the clipboard", content, mime)
	}
}

// The buttons are a list of struct fields beside a list of editors, which is
// exactly the shape that drifted before: keepDirEd arrived and nothing added it
// to the typing gate (keys_test.go). The same walk catches the same mistake here
// — a new settings box that silently cannot be pasted into, found by whoever is
// on a phone rather than by whoever added it.
//
// Two boxes deliberately have no buttons and are named here, so leaving a third
// out is a decision somebody has to write down (see clipboard.go).
func TestEverySettingsBoxHasClipboardButtons(t *testing.T) {
	noClipboard := map[string]string{
		"search":  "typed, and not a setting",
		"cacheEd": "four digits on a row that is already full",
	}

	v := reflect.ValueOf(testApp(t)).Elem()
	edType, clipType := reflect.TypeOf(widget.Editor{}), reflect.TypeOf(clipButtons{})
	var boxes []string
	pairs := 0
	for i := 0; i < v.NumField(); i++ {
		switch f := v.Type().Field(i); f.Type {
		case edType:
			if _, exempt := noClipboard[f.Name]; !exempt {
				boxes = append(boxes, f.Name)
			}
		case clipType:
			pairs++
		}
	}

	if len(boxes) == 0 {
		t.Fatal("found no settings text boxes at all — the walk is not looking at App")
	}
	if pairs != len(boxes) {
		t.Errorf("%d text boxes needing the clipboard (%s) but %d button pairs on App — "+
			"a box without them cannot be pasted into on a phone at all",
			len(boxes), strings.Join(boxes, ", "), pairs)
	}
}
