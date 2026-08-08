// Command gio-spike renders the toolkit-decision screen: a scrolling track list
// plus a player bar. It was built against a Fyne twin rendering the same rows
// from the same package, so that nothing but the toolkit could differ; Gio won
// and the twin is gone (../README.md keeps the measurements).
//
// This is still a spike, not a foundation: one flat list, a fake transport, no
// drill-down and no auth. Audio decoding is deliberately absent — it was the
// same burden in either toolkit, so it could not inform the choice.
package main

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"log"
	"os"
	"time"

	"gioui.org/app"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"daemonlord.ygg/madplayer/internal/madshare"
	"daemonlord.ygg/madplayer/spike/internal/demo"
)

type (
	C = layout.Context
	D = layout.Dimensions
)

func rgb(v uint32) color.NRGBA {
	return color.NRGBA{R: uint8(v >> 16), G: uint8(v >> 8), B: uint8(v), A: 0xff}
}

var (
	colBg     = rgb(0x14161a)
	colBar    = rgb(0x1b1e24)
	colLine   = rgb(0x2a2f38)
	colFg     = rgb(0xe8eaed)
	colDim    = rgb(0x9aa2ae)
	colAccent = rgb(0x4c8dff)
	colSel    = rgb(0x212936)
)

type ui struct {
	th     *material.Theme
	tracks []madshare.Track
	source string

	list   widget.List
	clicks []widget.Clickable

	prevBtn, playBtn, nextBtn widget.Clickable
	seek                      widget.Float

	current int
	playing bool
	elapsed float64
	last    time.Time
}

func main() {
	tracks, source := demo.Load(context.Background())
	go func() {
		w := new(app.Window)
		w.Option(
			app.Title("madplayer — Gio spike"),
			app.Size(unit.Dp(1000), unit.Dp(720)),
		)
		if err := run(w, tracks, source); err != nil {
			log.Fatal(err)
		}
		os.Exit(0)
	}()
	app.Main()
}

func run(w *app.Window, tracks []madshare.Track, source string) error {
	th := material.NewTheme()
	th.Palette = material.Palette{Bg: colBg, Fg: colFg, ContrastBg: colAccent, ContrastFg: rgb(0xffffff)}

	u := &ui{th: th, tracks: tracks, source: source, current: -1, clicks: make([]widget.Clickable, len(tracks))}
	u.list.Axis = layout.Vertical

	var ops op.Ops
	for {
		switch e := w.Event().(type) {
		case app.DestroyEvent:
			return e.Err
		case app.FrameEvent:
			gtx := app.NewContext(&ops, e)
			u.layout(gtx)
			e.Frame(gtx.Ops)
		}
	}
}

func (u *ui) total() float64 {
	if u.current < 0 || u.tracks[u.current].Duration == nil {
		return 0
	}
	return *u.tracks[u.current].Duration
}

func (u *ui) selectTrack(i int) {
	u.current = i
	u.elapsed = 0
	u.seek.Value = 0
	u.playing = true
	u.last = time.Now()
}

func (u *ui) step(i int) {
	if len(u.tracks) == 0 {
		return
	}
	n := u.current + i
	if n < 0 {
		n = len(u.tracks) - 1
	}
	if n >= len(u.tracks) {
		n = 0
	}
	u.selectTrack(n)
}

// update runs the transport before anything is laid out, so a click and the
// frame it affects are the same frame.
func (u *ui) update(gtx C) {
	if u.prevBtn.Clicked(gtx) {
		u.step(-1)
	}
	if u.nextBtn.Clicked(gtx) {
		u.step(1)
	}
	if u.playBtn.Clicked(gtx) {
		if u.current < 0 {
			u.selectTrack(0)
		} else {
			u.playing = !u.playing
			u.last = time.Now()
		}
	}

	if u.seek.Update(gtx) {
		u.elapsed = float64(u.seek.Value) * u.total()
		u.last = time.Now()
	}

	if u.playing && u.total() > 0 {
		u.elapsed += time.Since(u.last).Seconds()
		u.last = time.Now()
		if u.elapsed >= u.total() {
			u.step(1)
		}
		if !u.seek.Dragging() {
			u.seek.Value = float32(u.elapsed / u.total())
		}
		// Keep frames coming while the transport moves; without this the
		// window only redraws on input.
		gtx.Execute(op.InvalidateCmd{})
	}
}

func (u *ui) layout(gtx C) D {
	u.update(gtx)
	paint.FillShape(gtx.Ops, colBg, clip.Rect{Max: gtx.Constraints.Max}.Op())
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(u.header),
		layout.Flexed(1, u.trackList),
		layout.Rigid(u.playerBar),
	)
}

func (u *ui) header(gtx C) D {
	return layout.Stack{}.Layout(gtx,
		layout.Expanded(func(gtx C) D {
			paint.FillShape(gtx.Ops, colBar, clip.Rect{Max: gtx.Constraints.Min}.Op())
			return D{Size: gtx.Constraints.Min}
		}),
		layout.Stacked(func(gtx C) D {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return layout.Inset{Top: 14, Bottom: 14, Left: 20, Right: 20}.Layout(gtx, func(gtx C) D {
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Rigid(func(gtx C) D {
						l := material.H6(u.th, "madplayer")
						l.Color = colFg
						return l.Layout(gtx)
					}),
					layout.Rigid(func(gtx C) D {
						l := material.Caption(u.th, u.source)
						l.Color = colDim
						l.MaxLines = 1
						return l.Layout(gtx)
					}),
				)
			})
		}),
	)
}

func (u *ui) trackList(gtx C) D {
	lst := material.List(u.th, &u.list)
	lst.Indicator.Color = colLine
	return lst.Layout(gtx, len(u.tracks), u.row)
}

func (u *ui) row(gtx C, i int) D {
	if u.clicks[i].Clicked(gtx) {
		u.selectTrack(i)
	}
	t := u.tracks[i]
	sel := i == u.current

	return u.clicks[i].Layout(gtx, func(gtx C) D {
		return layout.Stack{}.Layout(gtx,
			layout.Expanded(func(gtx C) D {
				if sel {
					paint.FillShape(gtx.Ops, colSel, clip.Rect{Max: gtx.Constraints.Min}.Op())
				}
				return D{Size: gtx.Constraints.Min}
			}),
			layout.Stacked(func(gtx C) D {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				return layout.Inset{Top: 7, Bottom: 7, Left: 20, Right: 20}.Layout(gtx, func(gtx C) D {
					return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(func(gtx C) D {
							gtx.Constraints.Min.X = gtx.Dp(34)
							n := "·"
							if t.TrackNumber != nil {
								n = fmt.Sprintf("%d", *t.TrackNumber)
							}
							l := material.Body2(u.th, n)
							l.Color = colDim
							return l.Layout(gtx)
						}),
						layout.Flexed(1, func(gtx C) D {
							return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
								layout.Rigid(func(gtx C) D {
									l := material.Body1(u.th, t.Title)
									l.MaxLines = 1
									l.Color = colFg
									if sel {
										l.Color = colAccent
									}
									return l.Layout(gtx)
								}),
								layout.Rigid(func(gtx C) D {
									l := material.Caption(u.th, t.ArtistName+"  ·  "+t.AlbumTitle)
									l.MaxLines = 1
									l.Color = colDim
									return l.Layout(gtx)
								}),
							)
						}),
						layout.Rigid(func(gtx C) D {
							l := material.Body2(u.th, t.DurationString())
							l.Color = colDim
							return l.Layout(gtx)
						}),
					)
				})
			}),
		)
	})
}

func (u *ui) playerBar(gtx C) D {
	return layout.Stack{}.Layout(gtx,
		layout.Expanded(func(gtx C) D {
			paint.FillShape(gtx.Ops, colBar, clip.Rect{Max: gtx.Constraints.Min}.Op())
			line := image.Rect(0, 0, gtx.Constraints.Min.X, gtx.Dp(1))
			paint.FillShape(gtx.Ops, colLine, clip.Rect(line).Op())
			return D{Size: gtx.Constraints.Min}
		}),
		layout.Stacked(func(gtx C) D {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return layout.Inset{Top: 12, Bottom: 14, Left: 20, Right: 20}.Layout(gtx, func(gtx C) D {
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Rigid(u.nowPlaying),
					layout.Rigid(layout.Spacer{Height: 10}.Layout),
					layout.Rigid(u.transport),
				)
			})
		}),
	)
}

func (u *ui) nowPlaying(gtx C) D {
	title, artist := "Nothing playing", "pick a track"
	if u.current >= 0 {
		t := u.tracks[u.current]
		title, artist = t.Title, t.ArtistName+"  ·  "+t.AlbumTitle
	}
	return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
		layout.Flexed(1, func(gtx C) D {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(func(gtx C) D {
					l := material.Body1(u.th, title)
					l.MaxLines = 1
					l.Color = colFg
					return l.Layout(gtx)
				}),
				layout.Rigid(func(gtx C) D {
					l := material.Caption(u.th, artist)
					l.MaxLines = 1
					l.Color = colDim
					return l.Layout(gtx)
				}),
			)
		}),
		layout.Rigid(func(gtx C) D { return u.button(gtx, &u.prevBtn, "Prev") }),
		layout.Rigid(layout.Spacer{Width: 8}.Layout),
		layout.Rigid(func(gtx C) D {
			label := "Play"
			if u.playing {
				label = "Pause"
			}
			return u.button(gtx, &u.playBtn, label)
		}),
		layout.Rigid(layout.Spacer{Width: 8}.Layout),
		layout.Rigid(func(gtx C) D { return u.button(gtx, &u.nextBtn, "Next") }),
	)
}

func (u *ui) button(gtx C, click *widget.Clickable, label string) D {
	b := material.Button(u.th, click, label)
	b.Background = colSel
	b.Color = colFg
	b.CornerRadius = 6
	b.Inset = layout.Inset{Top: 8, Bottom: 8, Left: 16, Right: 16}
	return b.Layout(gtx)
}

func (u *ui) transport(gtx C) D {
	clock := func(sec float64) string {
		if u.current < 0 {
			return "—"
		}
		s := int(sec + 0.5)
		return fmt.Sprintf("%d:%02d", s/60, s%60)
	}
	return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			gtx.Constraints.Min.X = gtx.Dp(46)
			l := material.Caption(u.th, clock(u.elapsed))
			l.Color = colDim
			return l.Layout(gtx)
		}),
		layout.Flexed(1, material.Slider(u.th, &u.seek).Layout),
		layout.Rigid(func(gtx C) D {
			gtx.Constraints.Min.X = gtx.Dp(46)
			l := material.Caption(u.th, clock(u.total()))
			l.Color = colDim
			l.Alignment = text.End
			return l.Layout(gtx)
		}),
	)
}
