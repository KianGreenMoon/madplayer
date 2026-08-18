package ui

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gioui.org/io/clipboard"
	"gioui.org/layout"
	"gioui.org/widget/material"

	"daemonlord.ygg/madplayer/internal/logbuf"
)

// The Debugging page: the program's own log, on the screen, with the two ways
// off the device that need no cable.
//
// It exists because the log's usual homes both failed the 2026-08-18 crackle
// hunt. On the phone that matters, logcat's ring held ten minutes — the
// audio evidence was gone before a computer was near — and the app-private
// data dir where a file could go is exactly the place no file manager and no
// adb can read on a release build. So the page shows logbuf's ring directly,
// Copy puts the whole log on the clipboard (a paste into any message reaches
// a person who can read it), and Save writes a stamped file for the desktop,
// where the data dir IS reachable and a crash would take the clipboard down
// with the process.
//
// Newest first, deliberately: the reason this page is open is whatever just
// happened, and making somebody scroll two thousand lines to reach the
// starved-read they just heard is the logcat failure all over again. The
// copies stay oldest-first — a pasted log is read as a story.

// debugState is the index row's line: how much evidence there is.
func (a *App) debugState() string {
	return plural(logbuf.Count(), "log line")
}

// debugRows is the page: header, then one row per line so the page scrolls
// as the settings list it lives in — a list inside a list eats the outer
// one's scroll, the folders page's rule, applying here to two thousand rows.
func (a *App) debugRows(gtx C) []layout.Widget {
	if a.btnLogCopy.Clicked(gtx) {
		lines := logbuf.Snapshot()
		text := a.build.BuildLine() + "\n" + strings.Join(lines, "\n") + "\n"
		gtx.Execute(clipboard.WriteCmd{Type: clipMIME, Data: io.NopCloser(strings.NewReader(text))})
		a.setNotice("Copied " + plural(len(lines), "log line") + " to the clipboard")
	}
	if a.btnLogSave.Clicked(gtx) {
		if path, err := a.saveDebugLog(); err != nil {
			a.setNotice("Could not save the log: " + err.Error())
		} else {
			a.setNotice("Log saved: " + path)
		}
	}

	lines := logbuf.Snapshot()
	rows := make([]layout.Widget, 0, len(lines)+2)
	rows = append(rows, a.debugHeader)
	if len(lines) == 0 {
		rows = append(rows, func(gtx C) D {
			return a.sectionHint(gtx, "Nothing logged yet in this run.")
		})
		return rows
	}
	for i := len(lines) - 1; i >= 0; i-- {
		line := lines[i]
		rows = append(rows, func(gtx C) D { return a.debugLine(gtx, line) })
	}
	return rows
}

func (a *App) debugHeader(gtx C) D {
	return layout.Inset{Top: 18, Bottom: 8}.Layout(gtx, func(gtx C) D {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx C) D { return a.sectionTitle(gtx, "Debugging") }),
			layout.Rigid(func(gtx C) D {
				return a.sectionHint(gtx, "What madplayer has logged this run, newest first. "+
					"Copy and Save carry the whole log oldest-first, with the build line on top.")
			}),
			layout.Rigid(func(gtx C) D {
				return layout.Inset{Top: 8}.Layout(gtx, func(gtx C) D {
					return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(func(gtx C) D {
							return a.smallButton(gtx, &a.btnLogCopy, "Copy the log", false)
						}),
						layout.Rigid(layout.Spacer{Width: 8}.Layout),
						layout.Rigid(func(gtx C) D {
							return a.smallButton(gtx, &a.btnLogSave, "Save to a file", false)
						}),
					)
				})
			}),
		)
	})
}

// debugLine is one log line, small and wrapping. The whole point of the page
// is lines like "audio: feed starved …", so nothing is truncated.
func (a *App) debugLine(gtx C, line string) D {
	return layout.Inset{Top: 2}.Layout(gtx, func(gtx C) D {
		l := material.Caption(a.th, line)
		l.Color = colFg
		return l.Layout(gtx)
	})
}

// saveDebugLog writes the ring to a stamped file under the data dir — the
// one directory this program owns on every platform. On a phone that path is
// app-private (the notice still names it: it says the save worked and where
// a future, more reachable export would start from); on a desktop it is a
// plain file. The build line goes on top for the same reason About's copy
// carries it: a log that does not say which build wrote it answers nothing.
func (a *App) saveDebugLog() (string, error) {
	dir := filepath.Join(a.be.DataDir(), "logs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, time.Now().Format("log-20060102-150405.txt"))
	text := a.build.BuildLine() + "\n" + strings.Join(logbuf.Snapshot(), "\n") + "\n"
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		return "", err
	}
	return path, nil
}
