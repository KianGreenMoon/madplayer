package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gioui.org/layout"
	"gioui.org/widget"
	"gioui.org/widget/material"
)

// --- queue panel ------------------------------------------------------------

// queuePanel is the editable queue: click a row to play it, × to remove.
func (a *App) queuePanel(gtx C) D {
	items := a.pl.QueueItems()
	if len(items) == 0 {
		return a.emptyState(gtx, "The queue is empty. Play something from your library.")
	}

	if len(a.rmQueue) < len(items) {
		a.rmQueue = append(a.rmQueue, make([]widget.Clickable, len(items)-len(a.rmQueue))...)
	}
	a.ensureRows(len(items))
	cur := a.pl.QueueIndex()

	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			return layout.Inset{Top: 12, Bottom: 6, Left: 20, Right: 20}.Layout(gtx, func(gtx C) D {
				return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
					layout.Flexed(1, func(gtx C) D {
						l := material.Body2(a.th, fmt.Sprintf("%d in queue", len(items)))
						l.Color = colDim
						return l.Layout(gtx)
					}),
					layout.Rigid(func(gtx C) D {
						return a.smallButton(gtx, &a.btnClearQueue, "Clear", false)
					}),
				)
			})
		}),
		layout.Flexed(1, func(gtx C) D {
			lst := material.List(a.th, &a.queueList)
			lst.Indicator.Color = colLine
			return lst.Layout(gtx, len(items), func(gtx C, i int) D {
				it := items[i]
				if a.rows[i].Clicked(gtx) {
					a.pl.PlayIndex(i)
				}
				if a.rmQueue[i].Clicked(gtx) {
					a.pl.RemoveAt(i)
				}
				return a.row(gtx, &a.rows[i], i == cur, func(gtx C) D {
					return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
						layout.Flexed(1, func(gtx C) D {
							return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
								layout.Rigid(func(gtx C) D { return a.rowTitle(gtx, it.Title, i == cur) }),
								layout.Rigid(func(gtx C) D {
									l := material.Caption(a.th, it.Artist)
									l.Color = colDim
									l.MaxLines = 1
									return l.Layout(gtx)
								}),
							)
						}),
						layout.Rigid(func(gtx C) D {
							return a.smallButton(gtx, &a.rmQueue[i], "×", false)
						}),
					)
				})
			})
		}),
	)
}

// --- folders / settings -----------------------------------------------------

// settings is where the library comes from. There is no native folder picker in
// Gio, so the path is typed or pasted — and it is validated before being
// accepted, because a silently-ignored typo looks exactly like an empty library.
func (a *App) settings(gtx C) D {
	a.mu.Lock()
	roots := append([]string(nil), a.cfg.Roots...)
	status, scanning := a.status, a.scanning
	a.mu.Unlock()

	if len(a.rmFolder) < len(roots) {
		a.rmFolder = append(a.rmFolder, make([]widget.Clickable, len(roots)-len(a.rmFolder))...)
	}
	for i := range roots {
		if i < len(a.rmFolder) && a.rmFolder[i].Clicked(gtx) {
			a.removeRoot(roots[i])
		}
	}
	if a.btnAddFolder.Clicked(gtx) {
		a.addRoot(a.folderEd.Text())
	}
	if a.btnRescan.Clicked(gtx) && !scanning {
		a.Rescan()
	}

	return layout.Inset{Top: 16, Bottom: 16, Left: 20, Right: 20}.Layout(gtx, func(gtx C) D {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx C) D {
				l := material.Body1(a.th, "Music folders")
				l.Color = colFg
				return l.Layout(gtx)
			}),
			layout.Rigid(func(gtx C) D {
				return layout.Inset{Top: 4, Bottom: 12}.Layout(gtx, func(gtx C) D {
					l := material.Caption(a.th, "Scanned in place. Nothing is copied, moved or written to these folders.")
					l.Color = colDim
					return l.Layout(gtx)
				})
			}),

			layout.Rigid(func(gtx C) D {
				return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
					layout.Flexed(1, func(gtx C) D {
						ed := material.Editor(a.th, &a.folderEd, "/home/you/Music")
						ed.Color, ed.HintColor = colFg, colDim
						return filled(gtx, colSel, ed.Layout)
					}),
					layout.Rigid(layout.Spacer{Width: 8}.Layout),
					layout.Rigid(func(gtx C) D {
						return a.smallButton(gtx, &a.btnAddFolder, "Add folder", false)
					}),
					layout.Rigid(layout.Spacer{Width: 8}.Layout),
					layout.Rigid(func(gtx C) D {
						label := "Rescan"
						if scanning {
							label = "Scanning…"
						}
						return a.smallButton(gtx, &a.btnRescan, label, scanning)
					}),
				)
			}),

			layout.Rigid(func(gtx C) D {
				if status == "" {
					return D{}
				}
				return layout.Inset{Top: 10}.Layout(gtx, func(gtx C) D {
					l := material.Caption(a.th, status)
					l.Color = colDim
					return l.Layout(gtx)
				})
			}),

			layout.Rigid(layout.Spacer{Height: 16}.Layout),
			layout.Flexed(1, func(gtx C) D {
				if len(roots) == 0 {
					return a.emptyState(gtx, "No folders yet.")
				}
				return material.List(a.th, &a.queueList).Layout(gtx, len(roots), func(gtx C, i int) D {
					return layout.Inset{Top: 6, Bottom: 6}.Layout(gtx, func(gtx C) D {
						return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
							layout.Flexed(1, func(gtx C) D {
								l := material.Body2(a.th, roots[i])
								l.Color = colFg
								l.MaxLines = 1
								return l.Layout(gtx)
							}),
							layout.Rigid(func(gtx C) D {
								return a.smallButton(gtx, &a.rmFolder[i], "Remove", false)
							}),
						)
					})
				})
			}),
		)
	})
}

// addRoot validates before accepting. A path that does not exist, or is a file
// rather than a directory, is refused with a reason — the alternative is a
// scan that finds nothing and a user with no idea why.
func (a *App) addRoot(raw string) {
	path := strings.TrimSpace(raw)
	if path == "" {
		return
	}
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, path[2:])
		}
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		a.setStatus("Not a usable path: " + err.Error())
		return
	}
	info, err := os.Stat(abs)
	if err != nil {
		a.setStatus("Cannot open " + abs + ": " + err.Error())
		return
	}
	if !info.IsDir() {
		a.setStatus(abs + " is a file, not a folder.")
		return
	}

	a.mu.Lock()
	for _, r := range a.cfg.Roots {
		if r == abs {
			a.mu.Unlock()
			a.setStatus("Already watching " + abs)
			return
		}
	}
	a.cfg.Roots = append(a.cfg.Roots, abs)
	cfg := a.cfg
	a.mu.Unlock()

	a.folderEd.SetText("")
	_ = a.store.SaveConfig(cfg)
	a.Rescan()
}

func (a *App) removeRoot(path string) {
	a.mu.Lock()
	kept := a.cfg.Roots[:0]
	for _, r := range a.cfg.Roots {
		if r != path {
			kept = append(kept, r)
		}
	}
	a.cfg.Roots = kept
	cfg := a.cfg
	a.mu.Unlock()

	_ = a.store.SaveConfig(cfg)
	a.Rescan()
}

func (a *App) setStatus(msg string) {
	a.mu.Lock()
	a.status = msg
	a.mu.Unlock()
	a.win.Invalidate()
}
