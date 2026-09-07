// Command icon writes madplayer's launcher icon.
//
// One generator for both packagers: the Android build needs a launcher icon, and
// so does the freedesktop entry that puts the program in a menu and gives the
// desktop's media widget something to draw. The drawing itself is
// internal/icon, shared with the tray item, so the two cannot drift.
package main

import (
	"image/png"
	"log"
	"os"

	"daemonlord.ygg/madplayer/internal/icon"
)

const size = 512

func main() {
	out := "icon.png"
	if len(os.Args) > 1 {
		out = os.Args[1]
	}
	f, err := os.Create(out)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, icon.Draw(size)); err != nil {
		log.Fatal(err)
	}
}
