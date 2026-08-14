package prefs

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"daemonlord.ygg/madplayer/internal/queue"
)

// The play queue, as it survives a restart.
//
// It follows docs/ui/player-and-queue.md §"Persistence & resume", which the web
// UI already implements over localStorage: the visible order, the current index,
// the ORIGINAL un-shuffled order, the shuffle state, the repeat mode and the
// position within the current track. Two clients that disagree about what
// survives a restart disagree about what a queue is.
//
// It is a SEPARATE FILE from config.json, deliberately:
//
//   - config.json holds API tokens and is written 0600. The queue is a list of
//     song titles and holds nothing secret, and folding it in would rewrite the
//     credential file every five seconds while music plays.
//   - A queue that cannot be parsed must cost you the queue, not your sign-ins.
//     Separate files is what makes that true by construction rather than by
//     careful error handling.

// QueueState is everything needed to put the player back where it was.
type QueueState struct {
	// Items is the visible order — shuffled, if shuffle was on.
	Items []*queue.Item `json:"items"`
	// Original is the order the queue arrived in, kept so turning shuffle off
	// restores it. It is empty when shuffle was off, because it is then the same
	// list. queue.Restore relinks it by identity on the way back in.
	Original []*queue.Item `json:"original,omitempty"`
	Index    int           `json:"index"`
	Shuffled bool          `json:"shuffled,omitempty"`
	// Repeat is queue.Repeat as a number. Storing the enum rather than its name
	// is fine here because both ends are this program.
	Repeat int `json:"repeat,omitempty"`
	// Position is where the current track had got to, in seconds. It is applied
	// when playback is next STARTED, never on load — a player that resumed by
	// itself at launch would be a surprise, and a jarring one at 3am.
	Position float64 `json:"position,omitempty"`
}

func (s *Store) queuePath() string { return filepath.Join(s.Dir, "queue.json") }

// LoadQueue reads the saved queue. A missing file is an ordinary first run.
//
// A file that will not parse is reported, but the caller's answer is to carry on
// with an empty queue: losing a play queue is a small thing, and refusing to
// start a music player over one would not be.
func (s *Store) LoadQueue() (QueueState, error) {
	var q QueueState
	b, err := os.ReadFile(s.queuePath())
	if errors.Is(err, fs.ErrNotExist) {
		return q, nil
	}
	if err != nil {
		return q, err
	}
	if err := json.Unmarshal(b, &q); err != nil {
		return QueueState{}, fmt.Errorf("saved queue: %w", err)
	}
	if q.Index < 0 || q.Index >= len(q.Items) {
		// An index that does not name a track is not a queue we can restore
		// halfway: point at the start rather than at nothing.
		q.Index = 0
	}
	if q.Position < 0 {
		q.Position = 0
	}
	return q, nil
}

// SaveQueue writes it, replacing the file atomically so an interrupted write
// cannot leave half a queue behind — which is the shape most likely to be
// written, since this is saved while the program is running rather than only at
// exit.
//
// An empty queue REMOVES the file instead of writing an empty one. Clearing the
// queue is a thing people do to be rid of it, and leaving the last state on disk
// to be found later is not what they asked for.
func (s *Store) SaveQueue(q QueueState) error {
	if len(q.Items) == 0 {
		err := os.Remove(s.queuePath())
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	b, err := json.Marshal(q)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return err
	}
	tmp := s.queuePath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.queuePath())
}
