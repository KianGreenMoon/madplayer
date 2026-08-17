// Package ui is madplayer's Gio front end.
//
// It is an offline music player first: it scans folders, indexes what it finds
// and plays it, with no server, no account and no network. Reaching a madshare
// server is a feature layered on top of that, never a precondition — see
// docs/design.md.
//
// The browse behaviour follows docs/ui/library-page.md and
// docs/ui/artists-and-performers.md, and the queue follows
// docs/ui/player-and-queue.md. Those are cross-client contracts: where this
// disagrees with the web UI, the two stop being the same product.
package ui

import (
	"image/color"

	"gioui.org/app"
	"gioui.org/widget/material"
)

func rgb(v uint32) color.NRGBA {
	return color.NRGBA{R: uint8(v >> 16), G: uint8(v >> 8), B: uint8(v), A: 0xff}
}

// The colors every layout function reads. They are package variables rather
// than fields so eighty call sites did not have to grow a receiver; applyTheme
// swaps them as a set, on the UI goroutine, which is the only one that reads
// them.
var (
	colBg        = rgb(0x0d0d10)
	colBar       = rgb(0x16161c)
	colLine      = rgb(0x252530)
	colFg        = rgb(0xe2e2ea)
	colDim       = rgb(0x9090a8)
	colAccent    = rgb(0x7b68ee)
	colAccentDim = rgb(0x5548c8)
	colSel       = rgb(0x1e1e28)
	colWarn      = rgb(0xe0b24a)
	// colOnAccent is the glyph color on an accent-filled control. White in every
	// theme, matching the web UI's buttons (.ctrl-btn.primary is #fff on accent).
	colOnAccent = rgb(0xffffff)
)

// palette is one theme's colors, field for field the CSS variables of
// madshare's webui/static/css/app.css — same names, same values — so the two
// programs answer "what does Ocean look like" identically.
type palette struct {
	bg, surface, surfaceHover, text, textMuted color.NRGBA
	accent, accentDim, border, warning         color.NRGBA
}

// themeChoice is one entry of the Appearance section. name is what prefs
// stores and matches the web UI's data-theme value; label is what the chip
// says.
type themeChoice struct {
	name  string
	label string
	pal   palette
}

// themes is madshare's theme list, in its settings page's order. The first
// entry is the default, which is also what an empty or unknown saved name
// falls back to.
var themes = []themeChoice{
	{"dark", "Dark", palette{
		bg: rgb(0x0d0d10), surface: rgb(0x16161c), surfaceHover: rgb(0x1e1e28),
		text: rgb(0xe2e2ea), textMuted: rgb(0x9090a8),
		accent: rgb(0x7b68ee), accentDim: rgb(0x5548c8),
		border: rgb(0x252530), warning: rgb(0xe0b24a),
	}},
	{"light", "Light", palette{
		bg: rgb(0xf5f5f7), surface: rgb(0xffffff), surfaceHover: rgb(0xededf5),
		text: rgb(0x1a1a2e), textMuted: rgb(0x656580),
		accent: rgb(0x6c5ce7), accentDim: rgb(0x5048c0),
		border: rgb(0xdedee8), warning: rgb(0xb6791f),
	}},
	{"ocean", "Ocean", palette{
		bg: rgb(0x0a1628), surface: rgb(0x0f2040), surfaceHover: rgb(0x162a50),
		text: rgb(0xc8e6f5), textMuted: rgb(0x5d9ab8),
		accent: rgb(0x00cec9), accentDim: rgb(0x00a8a4),
		border: rgb(0x1a3050), warning: rgb(0xffd479),
	}},
	{"sunset", "Sunset", palette{
		bg: rgb(0x1a0a10), surface: rgb(0x26101a), surfaceHover: rgb(0x321525),
		text: rgb(0xf5e0d0), textMuted: rgb(0xc09080),
		accent: rgb(0xfd7c6e), accentDim: rgb(0xe05a4e),
		border: rgb(0x3a1820), warning: rgb(0xffcc70),
	}},
}

// paletteOf resolves a saved theme name, falling back to the default rather
// than to a half-set palette: the name in the file can predate a rename or
// simply be junk.
func paletteOf(name string) palette {
	for _, t := range themes {
		if t.name == name {
			return t.pal
		}
	}
	return themes[0].pal
}

// themeName is the resolved spelling of a saved name — what the Appearance
// chips highlight, so an unknown value lights the default instead of nothing.
func themeName(name string) string {
	for _, t := range themes {
		if t.name == name {
			return t.name
		}
	}
	return themes[0].name
}

func newTheme() *material.Theme {
	th := material.NewTheme()
	th.Palette = material.Palette{Bg: colBg, Fg: colFg, ContrastBg: colAccent, ContrastFg: colOnAccent}
	return th
}

// applyTheme installs a palette: the package colors, the material theme the
// widgets read, and Android's system bars, which are painted to match colBar
// (see Run). It does not save — setTheme is the choice, this is only the look.
func (a *App) applyTheme(name string) {
	p := paletteOf(name)
	colBg, colBar, colSel = p.bg, p.surface, p.surfaceHover
	colFg, colDim = p.text, p.textMuted
	colAccent, colAccentDim = p.accent, p.accentDim
	colLine, colWarn = p.border, p.warning
	a.th.Palette = material.Palette{Bg: colBg, Fg: colFg, ContrastBg: colAccent, ContrastFg: colOnAccent}
	a.win.Option(app.StatusColor(colBar), app.NavigationColor(colBar))
}

// setTheme is a click on an Appearance chip: the look changes this frame, and
// the choice is written down so the next launch opens the same way.
func (a *App) setTheme(name string) {
	a.applyTheme(name)
	a.mu.Lock()
	a.cfg.Theme = name
	cfg := a.cfg
	a.mu.Unlock()
	go func() {
		if err := a.store.Save(cfg); err != nil {
			a.setNoticeAsync("could not save the theme: " + err.Error())
		}
	}()
	a.win.Invalidate()
}
