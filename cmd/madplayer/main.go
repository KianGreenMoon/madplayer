// Command madplayer is a native music player.
//
// It plays what is on this machine: point it at your music folders and it
// scans, indexes and plays them, with no server, no account and no network.
// Reaching a madshare server is a feature layered on top — never a precondition.
//
// Design: docs/ui/madplayer.md.
package main

import (
	"log"
	"os"

	"gioui.org/app"
	"gioui.org/unit"

	"daemonlord.ygg/madplayer/internal/audio"
	"daemonlord.ygg/madplayer/internal/player"
	"daemonlord.ygg/madplayer/internal/ui"
)

func main() {
	go func() {
		if err := run(); err != nil {
			log.Fatal(err)
		}
		os.Exit(0)
	}()
	app.Main()
}

func run() error {
	pl, err := player.New(audio.New())
	if err != nil {
		// No audio device is fatal for a music player, and saying so plainly
		// beats a window that opens and then never makes a sound.
		return err
	}
	defer pl.Close()

	w := new(app.Window)
	w.Option(
		app.Title("madplayer"),
		app.Size(unit.Dp(1000), unit.Dp(720)),
	)
	return ui.New(w, pl).Run()
}
