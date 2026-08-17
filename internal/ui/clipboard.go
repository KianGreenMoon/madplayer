package ui

import (
	"io"
	"strings"

	"gioui.org/io/clipboard"
	"gioui.org/io/transfer"
	"gioui.org/layout"
	"gioui.org/widget"
	"gioui.org/widget/material"
)

// Copy and paste on a device with no keyboard.
//
// Gio puts the clipboard behind keyboard shortcuts and nothing else:
// widget.Editor answers Ctrl+C/V/X (widget/editor.go, Editor.command) and draws
// no selection toolbar of its own, and Android's long-press menu never reaches
// it because a Gio editor is not a native text view. On a phone that leaves
// every settings box WRITE-ONLY BY HAND — and the box that most needs the
// clipboard is the pairing one, which wants a 300-character node card that
// arrived in a message. Nobody is going to retype that.
//
// The rule that fixes it was already written here, for the keyboard bindings:
// nothing is reachable ONLY by key (keys.go). Ctrl+V was the one shortcut in
// this program with no visible twin. These buttons are that twin, and they are
// drawn at every width and on every platform for the same reason the bindings
// themselves are printed in Settings on every platform — a desktop simply ends
// up with two ways to do it.
//
// Underneath it is Gio's own paste rather than a second mechanism:
// clipboard.ReadCmd asks, a transfer.DataEvent answers, exactly as Ctrl+V does.
// What differs is where it lands. The editor INSERTS at the caret; these boxes
// each hold one whole value — a path, an address, a node card — so a paste
// REPLACES what is there, trimmed. Half a pasted address followed by the tail
// of the old one is not something anybody meant, and a newline off the end of a
// web page has broken a typed path before.
//
// Two boxes deliberately get none of this: the search box, which is typed and is
// not a setting, and the download limit, which is four digits on a row that is
// already full.

// clipMIME is the type both halves speak. It is the string Gio's own editor
// copies and pastes with, and the router matches on it — a different one here
// would ask for data no filter accepts.
const clipMIME = "application/text"

// clipButtons is one text box's pair of buttons. It lives on App beside the
// editor it belongs to, like every other widget in this program.
type clipButtons struct {
	copy, paste widget.Clickable
}

// clipRow lays out a settings text box with its clipboard buttons, followed by
// whatever action that section already had — so one call draws the whole row.
//
// canCopy is false for the password box: a phone's password manager needs to
// paste one IN, and lifting it back OUT onto a clipboard every other app can
// read is a use nobody has.
func (a *App) clipRow(gtx C, cb *clipButtons, ed *widget.Editor, hint string, canCopy bool, after ...layout.FlexChild) D {
	a.clipUpdate(gtx, cb, ed, canCopy)

	children := make([]layout.FlexChild, 0, 4+2*len(after))
	children = append(children, layout.Flexed(1, func(gtx C) D {
		e := material.Editor(a.th, ed, hint)
		e.Color, e.HintColor = colFg, colDim
		return filled(gtx, colSel, e.Layout)
	}))
	add := func(child layout.FlexChild) {
		children = append(children, layout.Rigid(layout.Spacer{Width: 8}.Layout), child)
	}
	add(layout.Rigid(func(gtx C) D { return a.actionButton(gtx, &cb.paste, iconPaste, false) }))
	// Copy arrives with the first character typed. A button that can be shown to
	// do nothing is what teaches somebody that these buttons do nothing.
	if canCopy && ed.Len() > 0 {
		add(layout.Rigid(func(gtx C) D { return a.actionButton(gtx, &cb.copy, iconCopy, false) }))
	}
	for _, child := range after {
		add(child)
	}
	return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx, children...)
}

// clipUpdate runs one box's buttons and takes delivery of a paste.
//
// The filter is registered EVERY frame rather than only after a click: the
// system answers a frame or more later — on Android across a JNI call — and a
// handler that has stopped listening by then is a paste that vanishes without a
// word.
func (a *App) clipUpdate(gtx C, cb *clipButtons, ed *widget.Editor, canCopy bool) {
	if cb.paste.Clicked(gtx) {
		gtx.Execute(clipboard.ReadCmd{Tag: cb})
	}
	if canCopy && cb.copy.Clicked(gtx) {
		// An empty box would hand the clipboard nothing and destroy whatever was
		// in it, which is the opposite of what the button offers.
		if s := ed.Text(); s != "" {
			gtx.Execute(clipboard.WriteCmd{Type: clipMIME, Data: io.NopCloser(strings.NewReader(s))})
			a.setNotice("Copied to the clipboard")
		}
	}
	for {
		ev, ok := gtx.Event(transfer.TargetFilter{Target: cb, Type: clipMIME})
		if !ok {
			return
		}
		data, isData := ev.(transfer.DataEvent)
		if !isData {
			continue // a cancelled transfer, which this side has nothing to undo
		}
		text, err := readAllAndClose(data.Open())
		if err != nil {
			a.setNotice("Could not read the clipboard: " + err.Error())
			continue
		}
		// Nothing to paste must leave the box alone. Emptying it would look
		// exactly like the paste having worked on a value that was blank.
		s := strings.TrimSpace(string(text))
		if s == "" {
			a.setNotice("The clipboard holds no text to paste")
			continue
		}
		// A paste is visible in the box the moment it lands, so it says nothing:
		// the notice line is for what a person cannot see for themselves.
		ed.SetText(s)
	}
}

// readAllAndClose drains a transfer's data. It is only valid to open one in the
// frame the event arrives, hence the read here and not in a goroutine.
func readAllAndClose(r io.ReadCloser) ([]byte, error) {
	defer r.Close()
	return io.ReadAll(r)
}
