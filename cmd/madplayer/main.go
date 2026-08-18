// Command madplayer is a native music player.
//
// It plays what is on this machine: point it at your music folders and it scans,
// indexes and plays them, with no server, no account and no network. The library
// engine is madshare, embedded in this process and called directly — same engine
// as the server, entered from the other end (docs/design.md).
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
	"daemonlord.ygg/madplayer/internal/logbuf"
	"daemonlord.ygg/madplayer/internal/player"
	"daemonlord.ygg/madplayer/internal/prefs"
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
	// First, before the sink opens: its "device rate" line is the one the
	// Debugging page must be able to show. From main rather than an init, so
	// Gio's Android log redirect (an init in gioui.org/app) is already in
	// place to be teed into.
	logbuf.Install()

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
	// The mesh switch has to be read before the backend starts, because whether
	// this device is a node is decided in the config the backend is built from.
	// There is no turning it on later without a restart, and pretending otherwise
	// would mean two ways to be a node that could disagree.
	settings, err := prefs.Default().Load()
	if err != nil {
		// Not fatal: a settings file that cannot be read costs the volume and the
		// server list, not the music. The UI reloads it and says so.
		log.Printf("madplayer: settings: %v", err)
	}
	// The backend is started before the window: a library that cannot be opened
	// is not something to discover after painting a frame, and the startup passes
	// take long enough on a large library to be worth doing once, up front.
	be, err := backend.Open(context.Background(), dir, log.Default(), backend.Options{
		Mesh:  settings.Mesh,
		Peers: settings.MeshPeers,
	})
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
