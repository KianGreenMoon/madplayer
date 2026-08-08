// Command madplayer is a native music player.
//
// It plays what is on this machine: point it at your music folders and it scans,
// indexes and plays them, with no server, no account and no network. The library
// engine is madshare, embedded in this process and called directly — same engine
// as the server, entered from the other end (docs/ui/madplayer.md).
package main

import (
	"context"
	"log"
	"os"
	"path/filepath"

	"gioui.org/app"
	"gioui.org/unit"

	"daemonlord.ygg/madplayer/internal/audio"
	"daemonlord.ygg/madplayer/internal/backend"
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

	dir, err := dataDir()
	if err != nil {
		return err
	}
	// The backend is started before the window: a library that cannot be opened
	// is not something to discover after painting a frame, and the startup passes
	// take long enough on a large library to be worth doing once, up front.
	be, err := backend.Open(context.Background(), dir, log.Default())
	if err != nil {
		return err
	}
	defer be.Close()

	w := new(app.Window)
	w.Option(
		app.Title("madplayer"),
		app.Size(unit.Dp(1000), unit.Dp(720)),
	)
	return ui.New(w, pl, be).Run()
}

// dataDir is where this install keeps its library: the database, the symlinks
// that point at the music, and later the madnetwork cache.
//
// app.DataDir is the toolkit's answer per platform — os.UserConfigDir on desktop,
// Context.getFilesDir on Android, NSDocumentDirectory on iOS — which is the only
// one that is right on a phone, where a hand-built $HOME path is not.
func dataDir() (string, error) {
	dir, err := app.DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "madplayer"), nil
}
