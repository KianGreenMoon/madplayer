package ui

import (
	"fmt"
	"image"

	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/widget/material"

	"daemonlord.ygg/madplayer/internal/queue"
)

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

// noticeLine carries the one-off messages: a failed track, or the undo offer
// after a hand-edited queue was replaced.
func (a *App) noticeLine(gtx C) D {
	if a.notice == "" {
		return D{}
	}
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
	}

	return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
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
		layout.Rigid(func(gtx C) D {
			return a.smallButton(gtx, &a.btnShuffle, "Shuffle", a.pl.Shuffled())
		}),
		layout.Rigid(layout.Spacer{Width: 6}.Layout),
		layout.Rigid(func(gtx C) D {
			r := a.pl.Repeat()
			return a.smallButton(gtx, &a.btnRepeat, "Repeat: "+r.String(), r != queue.RepeatOff)
		}),
		layout.Rigid(layout.Spacer{Width: 14}.Layout),
		layout.Rigid(func(gtx C) D { return a.smallButton(gtx, &a.btnPrev, "Prev", false) }),
		layout.Rigid(layout.Spacer{Width: 6}.Layout),
		layout.Rigid(func(gtx C) D {
			label := "Play"
			if a.pl.Playing() {
				label = "Pause"
			}
			return a.smallButton(gtx, &a.btnPlay, label, false)
		}),
		layout.Rigid(layout.Spacer{Width: 6}.Layout),
		layout.Rigid(func(gtx C) D { return a.smallButton(gtx, &a.btnNext, "Next", false) }),
	)
}

func (a *App) transport(gtx C, elapsed, total float64) D {
	return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			gtx.Constraints.Min.X = gtx.Dp(48)
			l := material.Caption(a.th, clock(elapsed))
			l.Color = colDim
			return l.Layout(gtx)
		}),
		layout.Flexed(1, material.Slider(a.th, &a.seek).Layout),
		layout.Rigid(func(gtx C) D {
			gtx.Constraints.Min.X = gtx.Dp(48)
			l := material.Caption(a.th, clock(total))
			l.Color = colDim
			l.Alignment = text.End
			return l.Layout(gtx)
		}),
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
