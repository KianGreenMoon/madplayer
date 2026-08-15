package ui

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"

	"daemonlord.ygg/madplayer/internal/library"
	"daemonlord.ygg/madplayer/internal/materialize"
	"daemonlord.ygg/madplayer/internal/queue"
)

// Keeping network music on this device.
//
// The rule is docs/ui/madplayer.md §"Where the bytes live": a track pulled off
// the network is copied into a folder madplayer manages, laid out from its tags,
// and handed to the library as an ordinary folder import — so it is the same
// kind of row as the music that was always here.
//
// The word on screen is "Keep on this device", and that is now the cross-client
// rule rather than this client's local preference: docs/ui/madnetwork-page.md
// decides the word by where the content LANDS — into a server's library it is
// Materialize, onto a person's own device it is this. Two acts, two words. A
// server is not "this device" and a player has no catalogue to admit anything
// to, so one word would have been wrong on one of them whichever won.

// startKeeper builds the keeper once the downloader exists.
//
// It is nil when there is no downloader — an install whose cache could not be
// opened — and every caller checks, because a player with no downloads still
// plays the music on this device, which is the whole posture of the program.
func (a *App) startKeeper() {
	if a.fetch == nil {
		return
	}
	a.mu.Lock()
	setting, technical := a.cfg.KeepDir, a.cfg.KeepTechnicalNames
	a.mu.Unlock()

	root, explicit := materialize.Resolve(setting, a.be.DataDir())
	keeper, err := materialize.NewKeeper(a.be.DataDir(), root, explicit, technical, a.fetch, a.be)
	if err != nil {
		// A record that would not parse costs the knowledge of what is ours, not
		// the ability to keep more. It is worth saying out loud, because the
		// visible symptom is a folder full of "somebody else put this here".
		log.Printf("madplayer: the record of kept music could not be read: %v", err)
	}

	a.mu.Lock()
	a.keeper = keeper
	a.mu.Unlock()
}

// reconcileKept brings the managed folder and the library back into agreement,
// in the background, and remembers what somebody else put in there.
func (a *App) reconcileKept() {
	a.mu.Lock()
	keeper := a.keeper
	a.mu.Unlock()
	if keeper == nil {
		return
	}

	survey, err := keeper.Reconcile(context.Background())
	if err != nil {
		log.Printf("madplayer: the kept-music folder could not be checked: %v", err)
	}
	a.mu.Lock()
	a.keepStrays = survey.Strays
	a.mu.Unlock()
	a.win.Invalidate()
}

// keepable reports whether a track is worth offering to keep: it plays from
// somewhere else, and there is a folder to put it in.
func (a *App) keepable(t *library.Track) bool {
	a.mu.Lock()
	keeper := a.keeper
	a.mu.Unlock()
	return keeper != nil && t != nil && t.Remote()
}

// keepTrack turns a browse row into the two things a keep needs: how to name it,
// and how to fetch it.
//
// The folder layout uses the ALBUM artist rather than the track's performer — a
// compilation belongs in one folder, not scattered across twelve — falling back
// to the track's own credit when the album artist is not known, which is what a
// search hit looks like.
func (a *App) keepTrack(t *library.Track, albumArtist string) (materialize.Track, *queue.Item, error) {
	best, ok := t.Best()
	if !ok {
		return materialize.Track{}, nil, errors.New(t.Title + " is not on this device right now")
	}
	artist := strings.TrimSpace(albumArtist)
	if artist == "" {
		artist = t.Artist
	}

	tr := materialize.Track{
		Artist: artist,
		Album:  t.Album,
		Title:  t.Title,
		Number: t.TrackNumber,
		Hash:   best.Hash,
		Ext:    best.Ext(),
	}
	items := a.itemsFromTracks([]*library.Track{t})
	return tr, items[0], nil
}

// keep saves tracks into the managed folder, one at a time, and says what
// happened.
//
// Sequential on purpose, and not only because the keeper serialises anyway: each
// one is a download, and starting twenty at once would compete for the same link
// and finish no sooner. It is the same shape madshare's own bulk materialize
// uses.
func (a *App) keep(tracks []*library.Track, albumArtist string) {
	a.mu.Lock()
	keeper, busy := a.keeper, a.keeping
	if keeper == nil {
		a.mu.Unlock()
		a.setNoticeAsync("Downloads are not available, so there is nowhere to keep this")
		return
	}
	if busy {
		a.mu.Unlock()
		a.setNoticeAsync("Still keeping the last one")
		return
	}
	a.keeping = true
	a.mu.Unlock()

	go func() {
		defer func() {
			a.mu.Lock()
			a.keeping = false
			a.mu.Unlock()
			a.win.Invalidate()
		}()

		ctx := context.Background()
		var saved, already int
		var failed []string

		for i, t := range tracks {
			if !t.Remote() {
				continue // already on this device: nothing to keep
			}
			a.setNoticeAsync(fmt.Sprintf("Keeping %s… (%d of %d)", t.Title, i+1, len(tracks)))

			tr, item, err := a.keepTrack(t, albumArtist)
			if err != nil {
				failed = append(failed, t.Title)
				continue
			}
			res, err := keeper.Keep(ctx, tr, item)
			switch {
			case errors.Is(err, materialize.ErrNotRemote):
				continue
			case err != nil:
				log.Printf("madplayer: keeping %s: %v", t.Title, err)
				failed = append(failed, t.Title)
			case res.Already:
				already++
			default:
				saved++
			}
		}

		a.setNoticeAsync(keptSentence(saved, already, failed, keeper.Root()))
		// The library gained rows, so whatever is on screen is now out of date.
		a.reload()
	}()
}

// keptSentence is what the notice line says afterwards.
//
// The three outcomes are three different sentences on purpose. "Already there"
// is not a failure and must not read like one, and a partial failure has to name
// the count rather than let a silent gap stand in for it.
func keptSentence(saved, already int, failed []string, root string) string {
	var parts []string
	if saved > 0 {
		parts = append(parts, fmt.Sprintf("Kept %s in %s", plural(saved, "track"), root))
	}
	if already > 0 {
		parts = append(parts, fmt.Sprintf("%s already there", plural(already, "track")))
	}
	if n := len(failed); n > 0 {
		if n == 1 {
			parts = append(parts, "could not keep "+failed[0])
		} else {
			parts = append(parts, "could not keep "+plural(n, "track"))
		}
	}
	if len(parts) == 0 {
		return "Nothing to keep — those tracks are already on this device"
	}
	return strings.Join(parts, " · ")
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// strayWarning is what Settings says about music somebody else put in the
// managed folder.
//
// It is a warning and not an action: the folder is the program's, so a stray is
// ignored — not adopted into the library, and certainly not moved somewhere else
// on its owner's behalf. Moving somebody's file is worse than refusing it.
func strayWarning(strays []string) string {
	if len(strays) == 0 {
		return ""
	}
	head := strays[0]
	if len(strays) == 1 {
		return fmt.Sprintf("%s is in this folder and was not put there by madplayer, so it is being ignored. Music of your own belongs in a folder you add yourself.", head)
	}
	return fmt.Sprintf("%s and %d other file(s) here were not put there by madplayer, so they are being ignored. Music of your own belongs in a folder you add yourself.", head, len(strays)-1)
}
