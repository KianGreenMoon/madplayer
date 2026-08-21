package ui

import (
	"fmt"
	"image"
	"time"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/widget/material"

	"daemonlord.ygg/madplayer/internal/queue"
)

// originOr names a library, or says something rather than nothing.
func originOr(label, fallback string) string {
	if label == "" {
		return fallback
	}
	return label
}

func clock(sec float64) string {
	if sec <= 0 {
		return "0:00"
	}
	s := int(sec + 0.5)
	if h := s / 3600; h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, (s%3600)/60, s%60)
	}
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

func (a *App) playerBar(gtx C) D {
	// Position falls back to the queue item's duration when the decoder cannot
	// know the total (a streaming mp3) — inside the player, so the bar, the
	// keyboard seek and the media bus all read the same number.
	elapsed, total := a.pl.Position()
	if !a.seeking && total > 0 {
		a.seek.Value = float32(elapsed / total)
	}

	return layout.Stack{}.Layout(gtx,
		layout.Expanded(func(gtx C) D {
			paint.FillShape(gtx.Ops, colBar, clip.Rect{Max: gtx.Constraints.Min}.Op())
			line := image.Rect(0, 0, gtx.Constraints.Min.X, gtx.Dp(1))
			paint.FillShape(gtx.Ops, colLine, clip.Rect(line).Op())
			return D{Size: gtx.Constraints.Min}
		}),
		layout.Stacked(func(gtx C) D {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return layout.Inset{Top: 10, Bottom: 12, Left: 20, Right: 20}.Layout(gtx, func(gtx C) D {
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Rigid(a.noticeLine),
					layout.Rigid(a.nowPlaying),
					layout.Rigid(layout.Spacer{Height: 8}.Layout),
					layout.Rigid(func(gtx C) D { return a.transport(gtx, elapsed, total) }),
				)
			})
		}),
	)
}

// noticeLife is how long a one-off message is worth the line it costs.
//
// Long enough to read a sentence and reach the Undo beside it, short enough that
// the player bar is not still explaining a queue edit from ten minutes ago —
// which is what it did until 2026-08-15, because nothing ever cleared it.
const noticeLife = 10 * time.Second

// noticeLine carries the one-off messages: a failed track, or the undo offer
// after a hand-edited queue was replaced.
//
// It expires itself. The frame that retires it is asked for up front
// (InvalidateCmd), because an idle player draws nothing on its own and a message
// that only vanishes when something else happens to repaint is a message that
// stays until the next click.
func (a *App) noticeLine(gtx C) D {
	if a.notice == "" {
		return D{}
	}
	left := noticeLife - time.Since(a.noticeAt)
	if left <= 0 {
		a.notice = ""
		return D{}
	}
	gtx.Execute(op.InvalidateCmd{At: gtx.Now.Add(left)})
	return layout.Inset{Bottom: 8}.Layout(gtx, func(gtx C) D {
		return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
			layout.Flexed(1, func(gtx C) D {
				l := material.Caption(a.th, a.notice)
				l.Color = colWarn
				l.MaxLines = 1
				return l.Layout(gtx)
			}),
			layout.Rigid(func(gtx C) D {
				if !a.pl.CanUndo() {
					return D{}
				}
				return a.smallButton(gtx, &a.btnUndo, "Undo", false)
			}),
		)
	})
}

func (a *App) nowPlaying(gtx C) D {
	title, sub := "Nothing playing", "pick a track"
	if cur := a.pl.Current(); cur != nil {
		title = cur.Title
		sub = cur.Artist
		if cur.Album != "" {
			sub += "  ·  " + cur.Album
		}
		// The origin is desktop-only decoration: at phone width the one
		// subtitle line belongs to the artist and the album (owner's call,
		// 2026-08-17). The Downloading message below keeps naming it
		// everywhere, because there it explains a wait.
		if cur.Origin != "" && !a.narrowUI {
			sub += "  ·  " + cur.Origin
		}
		// A download takes as long as it takes, and silence with no explanation
		// is indistinguishable from a hang.
		if a.pl.Loading() {
			sub = "Downloading from " + originOr(cur.Origin, "the server") + "…"
		}
	}

	info := func(gtx C) D {
		return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
			// The cover is the one place a player is expected to show one, and it is
			// laid out even when there is none: a tile that appears and disappears
			// would move the whole transport sideways every time a track changed.
			layout.Rigid(func(gtx C) D { return a.cover(gtx, a.nowPlayingCoverKey(), 44) }),
			layout.Rigid(layout.Spacer{Width: 12}.Layout),
			layout.Flexed(1, func(gtx C) D {
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Rigid(func(gtx C) D {
						l := material.Body1(a.th, title)
						l.MaxLines = 1
						l.Color = colFg
						return l.Layout(gtx)
					}),
					layout.Rigid(func(gtx C) D {
						l := material.Caption(a.th, sub)
						l.MaxLines = 1
						l.Color = colDim
						return l.Layout(gtx)
					}),
				)
			}),
		)
	}
	// The transport is icons in the web UI's order — shuffle · prev · play ·
	// next · repeat — with the same statement of state: an engaged mode holds
	// the accent, and play is the one accent-filled circle on the bar.
	buttons := []layout.FlexChild{
		layout.Rigid(func(gtx C) D {
			return a.ctrlButton(gtx, &a.btnShuffle, iconShuffle, 36, a.pl.Shuffled())
		}),
		layout.Rigid(layout.Spacer{Width: 4}.Layout),
		layout.Rigid(func(gtx C) D { return a.ctrlButton(gtx, &a.btnPrev, iconPrev, 36, false) }),
		layout.Rigid(layout.Spacer{Width: 8}.Layout),
		layout.Rigid(func(gtx C) D {
			ic := iconPlay
			switch {
			case a.pl.Loading():
				ic = iconLoading
			case a.pl.Playing():
				ic = iconPause
			}
			return a.primaryButton(gtx, &a.btnPlay, ic, 42)
		}),
		layout.Rigid(layout.Spacer{Width: 8}.Layout),
		layout.Rigid(func(gtx C) D { return a.ctrlButton(gtx, &a.btnNext, iconNext, 36, false) }),
		layout.Rigid(layout.Spacer{Width: 4}.Layout),
		layout.Rigid(func(gtx C) D {
			// Repeat-one is its own glyph rather than a badge: Gio ships it,
			// where the web UI has to fake one with CSS.
			r := a.pl.Repeat()
			ic := iconRepeat
			if r == queue.RepeatOne {
				ic = iconRepeatOne
			}
			return a.ctrlButton(gtx, &a.btnRepeat, ic, 36, r != queue.RepeatOff)
		}),
		layout.Rigid(layout.Spacer{Width: 4}.Layout),
		layout.Rigid(func(gtx C) D {
			// The queue lives on the bar like the web UI's #btnQueue: right of
			// the transport, holding the accent while its panel is open.
			return a.ctrlFrame(gtx, &a.btnQueue, 36, a.view == viewQueue, queueGlyph)
		}),
	}

	// One row on a desktop, two on a phone. Five rigid buttons beside a
	// flexed title works out to the title losing: on a phone-wide window it
	// was squeezed to its first glyph and Next was pushed off screen
	// entirely, so below this width the buttons take a row of their own and
	// the title gets the full width.
	if gtx.Constraints.Max.X < gtx.Dp(narrowBar) {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(info),
			layout.Rigid(layout.Spacer{Height: 8}.Layout),
			layout.Rigid(func(gtx C) D {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle,
					Spacing: layout.SpaceSides}.Layout(gtx, buttons...)
			}),
		)
	}
	row := append([]layout.FlexChild{layout.Flexed(1, info)}, buttons...)
	return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx, row...)
}

// narrowBar is the width below which the player bar stops pretending to be on
// a desktop: the buttons move under the title, and the volume slider yields
// its space to the seek bar — a phone has volume keys on its edge.
const narrowBar = 640

func (a *App) transport(gtx C, elapsed, total float64) D {
	children := []layout.FlexChild{
		layout.Rigid(func(gtx C) D {
			gtx.Constraints.Min.X = gtx.Dp(48)
			l := material.Caption(a.th, clock(elapsed))
			l.Color = colDim
			return l.Layout(gtx)
		}),
		layout.Flexed(1, func(gtx C) D {
			// A streaming track scrubs too now (player.seekStream restarts the
			// decode at the target), so the slider only goes quiet when there is
			// genuinely nothing to aim into: no track open, or no total to map a
			// drag onto — a streaming mp3 from a server that never analyzed it.
			sl := material.Slider(a.th, &a.seek)
			if !a.pl.Seekable() || total <= 0 {
				sl.Color = colLine
				// Disabled() is a context that delivers no events, so the thumb
				// cannot be dragged at all — better than dragging it into a seek
				// that is silently refused further down.
				return sl.Layout(gtx.Disabled())
			}
			return sl.Layout(gtx)
		}),
		layout.Rigid(func(gtx C) D {
			gtx.Constraints.Min.X = gtx.Dp(48)
			l := material.Caption(a.th, clock(total))
			l.Color = colDim
			l.Alignment = text.End
			return l.Layout(gtx)
		}),
	}
	if gtx.Constraints.Max.X >= gtx.Dp(narrowBar) {
		children = append(children,
			layout.Rigid(layout.Spacer{Width: 16}.Layout),
			layout.Rigid(func(gtx C) D {
				l := material.Caption(a.th, "Vol")
				l.Color = colDim
				return l.Layout(gtx)
			}),
			layout.Rigid(layout.Spacer{Width: 8}.Layout),
			layout.Rigid(func(gtx C) D {
				gtx.Constraints.Min.X = gtx.Dp(90)
				gtx.Constraints.Max.X = gtx.Dp(90)
				return material.Slider(a.th, &a.vol).Layout(gtx)
			}),
		)
	}
	return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx, children...)
}
