package ui

import (
	"log"
	"time"

	"daemonlord.ygg/madplayer/internal/prefs"
	"daemonlord.ygg/madplayer/internal/queue"
)

// The play queue across restarts.
//
// docs/ui/player-and-queue.md §"Persistence & resume" has had this rule since
// the web UI shipped, and the pieces on this side were built and never wired:
// player.Snapshot and player.Restore existed with nothing calling them, so
// closing the window threw the queue away. The contract, followed here:
//
//   - the visible order, the current index, the ORIGINAL un-shuffled order, the
//     shuffle state, the repeat mode and the position within the current track;
//   - written on every queue change, every few seconds while playing, and at exit;
//   - restored PAUSED, pointing at the track. Pressing play resumes mid-track;
//     clicking a row starts that row from the beginning, as usual.
//
// A player that started making noise by itself at launch would be a surprise,
// and a bad one at three in the morning.

// queueSaveEvery is the heartbeat that keeps the saved POSITION current.
//
// Everything else about the queue is saved when it changes; the position moves
// continuously and there is nothing to hook. Five seconds is what the web UI
// settled on for the same reason, and it bounds what a crash costs to five
// seconds of a song.
const queueSaveEvery = 5 * time.Second

// restoreQueue puts the last session's queue back, paused.
func (a *App) restoreQueue() {
	saved, err := a.store.LoadQueue()
	if err != nil {
		// The queue is the one thing here that is cheap to lose. Say so and
		// carry on: refusing to start a music player over an unreadable list of
		// song titles would not be a trade anybody wants.
		log.Printf("madplayer: the saved queue could not be read (starting empty): %v", err)
		return
	}
	if len(saved.Items) == 0 {
		return
	}
	a.pl.Restore(saved.Items, saved.Original, saved.Index, saved.Shuffled, queue.RepeatFrom(saved.Repeat))
	a.pl.ResumeAt(saved.Position)
	a.win.Invalidate()
}

// markQueueDirty asks for a save without doing one.
//
// It is called from the player's change hook, which runs on the player's own
// goroutine — writing a file there would put a disk on the path of every queue
// operation. The channel has room for one, so a burst of changes collapses into
// a single write.
func (a *App) markQueueDirty() {
	select {
	case a.saveQueue <- struct{}{}:
	default:
	}
}

// queueSaver is the one writer. Having exactly one is what keeps two saves from
// interleaving into a file that is neither.
func (a *App) queueSaver() {
	tick := time.NewTicker(queueSaveEvery)
	defer tick.Stop()
	for {
		select {
		case <-a.done:
			return
		case <-a.saveQueue:
			a.writeQueue()
		case <-tick.C:
			// The position only moves while something is playing, so a paused
			// player writes nothing at all.
			if a.pl.Playing() {
				a.writeQueue()
			}
		}
	}
}

// writeQueue snapshots the queue and puts it on disk.
func (a *App) writeQueue() {
	items, original, index, shuffled, repeat := a.pl.Snapshot()
	elapsed, _ := a.pl.Position()
	err := a.store.SaveQueue(prefs.QueueState{
		Items:    items,
		Original: original,
		Index:    index,
		Shuffled: shuffled,
		Repeat:   int(repeat),
		Position: elapsed,
	})
	if err != nil {
		log.Printf("madplayer: the queue could not be saved: %v", err)
	}
}
