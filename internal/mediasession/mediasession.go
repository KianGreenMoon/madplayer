// Package mediasession puts this player on Android's media surfaces.
//
// It is internal/mpris told again on the other platform: the MediaSession is
// what fills the lock screen and the quick-settings media carousel, what makes
// Bluetooth and wired-headset buttons reach THIS program, and — through the
// foreground service that carries it — what keeps Android from freezing the
// process the moment the screen goes dark. Without it, playback dies about a
// second after the display sleeps, which is the difference between a music
// player and a demo.
//
// The same two properties as MPRIS shape it:
//
//   - **It is optional and must never be fatal.** On every platform but
//     Android the package is a stub that does nothing, and on Android a
//     missing Java half (a build without the jar) costs the background
//     controls, never the program.
//   - **It is a VIEW, not a second player.** Every push is computed from the
//     Controls interface when something changes, and every control gesture
//     from the notification, the lock screen or a headset button comes back
//     through here to the same player the window drives. Neither side caches
//     what the other decides.
//
// The split across the three files mirrors what can be VERIFIED where. This
// file and its tests build everywhere, so the state logic is pinned on the
// desktop; service_android.go is only the JNI plumbing, because it can only be
// compiled by the Android build host; PlaybackService.java is the Android half
// and rides in as a jar built by android/build-apk.sh.
package mediasession

import "daemonlord.ygg/madplayer/internal/queue"

// Controls is the player, as the Android media surfaces need it.
//
// The method set is the answer to "what may the lock screen do", same as the
// MPRIS twin — and it is deliberately smaller: no volume (the phone's rocker
// owns that), no shuffle or repeat (Android's media controls carry no such
// buttons), no queue introspection.
type Controls interface {
	Play()
	Pause()
	Stop()
	Next()
	Prev()

	Playing() bool
	Paused() bool
	Loading() bool
	Position() (elapsed, total float64)
	Seek(seconds float64)

	Current() *queue.Item

	// ArtPath is a file holding the current track's cover, or "". A file and
	// not an image for the MPRIS reason in a new coat: the bitmap is decoded
	// by the Java side, which cannot see this process's memory any more
	// usefully than another process could.
	ArtPath() string
}

// snapshot is one observation of the player, complete enough that the Java
// side never has to ask anything back. Update computes one; the Android loop
// diffs consecutive ones to decide what to push over JNI.
type snapshot struct {
	active     bool // a track is open — the service should exist
	playing    bool // moving, or loading with intent to move
	title      string
	artist     string
	album      string
	artPath    string
	durationMs int64
	positionMs int64
}

// sameTrack reports whether the metadata push may be skipped. Position and
// playing are deliberately not compared — they change constantly and travel in
// the state push, which is always sent.
func (s snapshot) sameTrack(o snapshot) bool {
	return s.title == o.title && s.artist == o.artist && s.album == o.album &&
		s.artPath == o.artPath && s.durationMs == o.durationMs
}

// observe reads the player once. The active/playing mapping is the MPRIS
// status rule: a LOADING track counts as playing, because the person pressed
// play and the lock screen saying "paused" would look like the command was
// lost; a current item that is neither playing nor paused is stopped, and
// stopped means no service and no notification.
func observe(c Controls) snapshot {
	cur := c.Current()
	if cur == nil {
		return snapshot{}
	}
	playing := c.Playing() || c.Loading()
	if !playing && !c.Paused() {
		return snapshot{}
	}
	snap := snapshot{
		active:  true,
		playing: playing,
		title:   cur.Title,
		artist:  cur.Artist,
		album:   cur.Album,
		artPath: c.ArtPath(),
	}
	elapsed, total := c.Position()
	snap.positionMs = int64(elapsed * 1000)
	if total > 0 {
		snap.durationMs = int64(total * 1000)
	} else if cur.Duration > 0 {
		snap.durationMs = int64(cur.Duration * 1000)
	}
	return snap
}
