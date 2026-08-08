package ui

import (
	"context"
	"fmt"
	"strings"

	"gioui.org/layout"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"daemonlord.ygg/madplayer/internal/backend"
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
									sub := it.Artist
									// Which library a queued track plays from was
									// decided when it was queued, so the queue is
									// where it is worth saying.
									if it.Origin != "" {
										sub += "  ·  " + it.Origin
									}
									l := material.Caption(a.th, sub)
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
	folders := append([]backend.Folder(nil), a.folders...)
	status, scanning := a.status, a.scanning
	a.mu.Unlock()

	if len(a.rmFolder) < len(folders) {
		a.rmFolder = append(a.rmFolder, make([]widget.Clickable, len(folders)-len(a.rmFolder))...)
	}
	for i := range folders {
		if i < len(a.rmFolder) && a.rmFolder[i].Clicked(gtx) {
			a.removeFolder(folders[i])
		}
	}
	if a.btnAddFolder.Clicked(gtx) {
		a.addFolder(a.folderEd.Text())
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
				if len(folders) == 0 {
					return a.emptyState(gtx, "No folders yet.")
				}
				return material.List(a.th, &a.queueList).Layout(gtx, len(folders), func(gtx C, i int) D {
					return layout.Inset{Top: 6, Bottom: 6}.Layout(gtx, func(gtx C) D {
						f := folders[i]
						return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
							layout.Flexed(1, func(gtx C) D {
								return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
									layout.Rigid(func(gtx C) D {
										l := material.Body2(a.th, f.Path)
										l.Color = colFg
										l.MaxLines = 1
										return l.Layout(gtx)
									}),
									layout.Rigid(func(gtx C) D {
										l := material.Caption(a.th, describeFolder(f))
										l.Color = colDim
										if f.Missing || f.Status == "error" {
											l.Color = colWarn
										}
										l.MaxLines = 1
										return l.Layout(gtx)
									}),
								)
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

// describeFolder is a folder's second line: how it went, or why it cannot be
// read right now.
//
// A folder that is not currently mounted says so in those words. On a server a
// vanished import is an incident; on a player it is an unplugged drive, and
// calling that "0 tracks" or "error" would blame the user for ejecting a card.
func describeFolder(f backend.Folder) string {
	switch {
	case f.Scanning():
		return "Scanning…"
	case f.Missing:
		return "Not connected right now — nothing was removed from your library"
	case f.Status == "error":
		return "Could not be read"
	}
	s := fmt.Sprintf("%d tracks", f.Tracks)
	if f.Failed > 0 {
		s += fmt.Sprintf(" · %d unreadable", f.Failed)
	}
	if f.ScannedAt == 0 {
		s += " · not scanned yet"
	}
	return s
}

// addFolder imports a typed path in place. The backend validates it (and says
// why when it refuses), so the only thing done here is clearing the box on
// success — a path that vanishes from the field looks accepted, and one that
// stays with a message next to it looks refused, which is exactly the truth in
// each case.
func (a *App) addFolder(raw string) {
	if strings.TrimSpace(raw) == "" {
		return
	}
	go func() {
		f, err := a.be.AddFolder(context.Background(), raw)
		if err != nil {
			a.setStatus(err.Error())
			return
		}
		a.folderEd.SetText("")
		a.setStatus("Importing " + f.Path + "…")
		a.watchScan()
	}()
}

// removeFolder forgets a folder. The originals are untouched: this removes
// library entries, never music.
func (a *App) removeFolder(f backend.Folder) {
	go func() {
		if err := a.be.RemoveFolder(context.Background(), f.ID); err != nil {
			a.setStatus(err.Error())
			return
		}
		a.setStatus("Removed " + f.Path + " — the files themselves are untouched")
		a.loadFolders()
		a.reload()
	}()
}
