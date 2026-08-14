// Package mpris puts this player on the desktop's media bus.
//
// MPRIS (org.mpris.MediaPlayer2) is how a Linux desktop knows what is playing:
// it is what makes the XF86Audio keys on a keyboard reach THIS program rather
// than nothing, what fills the media widget in GNOME's calendar drop-down and
// KDE's system tray, and what `playerctl` speaks. Without it, a music player
// that is not the front window is a program you have to go and find.
//
// Two properties shape the whole package:
//
//   - **It is optional and must never be fatal.** There is no session bus on a
//     headless machine, in a minimal container, or on any other platform, and a
//     music player that refuses to start over a missing bus would be absurd.
//     Every failure here is reported once and then ignored.
//   - **It is a VIEW, not a second player.** Nothing here holds playback state.
//     Every property is computed from the Controls interface when asked, so the
//     bus and the window can never disagree about what is playing — which is the
//     failure mode of caching it.
package mpris

import (
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"sync"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/introspect"
	"github.com/godbus/dbus/v5/prop"

	"daemonlord.ygg/madplayer/internal/queue"
)

// Controls is the player, as MPRIS needs it.
//
// It is an interface rather than the concrete player for the usual reason —
// this package is tested with no audio device and no bus — but also because the
// method set IS the answer to "what may a remote control do", and writing it
// down is what keeps that from being "anything".
type Controls interface {
	Play()
	Pause()
	Toggle()
	Stop()
	Next()
	Prev()

	Playing() bool
	Paused() bool
	Loading() bool
	Position() (elapsed, total float64)
	Seek(seconds float64)

	Volume() float64
	SetVolume(float64)

	Shuffled() bool
	SetShuffle(bool)
	Repeat() queue.Repeat
	SetRepeat(queue.Repeat)

	Current() *queue.Item
	QueueLen() int
	QueueIndex() int

	// ArtPath is a file holding the current track's cover, or "".
	//
	// A file and not an image, because mpris:artUrl is a URL: the desktop's
	// media widget fetches it in another process, so bytes in this one's memory
	// are of no use to it. It is asked for per update rather than held, since a
	// cover that is still being read has no file yet and gains one a moment
	// later — the next Update picks it up.
	ArtPath() string
}

const (
	busPrefix = "org.mpris.MediaPlayer2."
	objPath   = "/org/mpris/MediaPlayer2"
	rootIface = "org.mpris.MediaPlayer2"
	playIface = "org.mpris.MediaPlayer2.Player"
)

// Service is this player's presence on the bus. A nil Service is usable and
// does nothing, which is what lets the caller ignore a failed New.
type Service struct {
	conn  *dbus.Conn
	props *prop.Properties
	c     Controls

	// mu guards the only bookkeeping that is genuinely ours: the track id, an
	// identity the bus requires and the queue has no notion of.
	mu       sync.Mutex
	trackNo  int64
	lastKey  string
	quitOnce sync.Once
	onQuit   func()
}

// New announces the player and starts answering for it.
//
// name becomes org.mpris.MediaPlayer2.<name>. onQuit is called when the desktop
// asks the player to quit — it is a real request from a real user gesture, so
// refusing it would be rude, but only the program itself knows how to shut down.
func New(name string, c Controls, onQuit func()) (*Service, error) {
	conn, err := dbus.SessionBus()
	if err != nil {
		return nil, fmt.Errorf("no session bus: %w", err)
	}

	s := &Service{conn: conn, c: c, onQuit: onQuit}
	if err := s.export(); err != nil {
		return nil, err
	}

	// The bus name is what the desktop looks the player up by. Losing the race
	// for it means another madplayer is already there — which is not an error
	// worth failing over, but it does mean this instance is not the one the
	// media keys will reach. The spec's answer is the .instanceNNN suffix.
	full := busPrefix + name
	reply, err := conn.RequestName(full, dbus.NameFlagDoNotQueue)
	if err != nil {
		return nil, fmt.Errorf("requesting %s: %w", full, err)
	}
	if reply != dbus.RequestNameReplyPrimaryOwner {
		full = fmt.Sprintf("%s%s.instance%d", busPrefix, name, os.Getpid())
		if reply, err = conn.RequestName(full, dbus.NameFlagDoNotQueue); err != nil {
			return nil, fmt.Errorf("requesting %s: %w", full, err)
		}
		if reply != dbus.RequestNameReplyPrimaryOwner {
			return nil, errors.New("another player already owns " + full)
		}
	}
	return s, nil
}

// export publishes the two interfaces, their properties and the introspection
// data. A client that cannot introspect us is a client that shows nothing.
func (s *Service) export() error {
	if err := s.conn.Export(rootHandler{s}, objPath, rootIface); err != nil {
		return err
	}
	if err := s.conn.ExportWithMap(playerHandler{s}, playerMethodNames, objPath, playIface); err != nil {
		return err
	}

	props, err := prop.Export(s.conn, objPath, s.propMap())
	if err != nil {
		return err
	}
	s.props = props

	node := &introspect.Node{
		Name: objPath,
		Interfaces: []introspect.Interface{
			introspect.IntrospectData,
			prop.IntrospectData,
			{
				Name:       rootIface,
				Methods:    introspect.Methods(rootHandler{s}),
				Properties: props.Introspection(rootIface),
			},
			{
				Name:       playIface,
				Methods:    playerMethods(),
				Properties: props.Introspection(playIface),
				Signals: []introspect.Signal{{
					Name: "Seeked",
					Args: []introspect.Arg{{Name: "Position", Type: "x"}},
				}},
			},
		},
	}
	return s.conn.Export(introspect.NewIntrospectable(node), objPath, "org.freedesktop.DBus.Introspectable")
}

// Close releases the bus name.
func (s *Service) Close() error {
	if s == nil || s.conn == nil {
		return nil
	}
	return s.conn.Close()
}

// --- properties -------------------------------------------------------------

func (s *Service) propMap() prop.Map {
	ro := func(v any) *prop.Prop { return &prop.Prop{Value: v, Emit: prop.EmitTrue} }

	return prop.Map{
		rootIface: {
			"CanQuit":             ro(true),
			"CanRaise":            ro(false), // Gio cannot raise its own window
			"HasTrackList":        ro(false), // the queue is not published as a TrackList
			"Identity":            ro("madplayer"),
			"DesktopEntry":        ro("madplayer"),
			"SupportedUriSchemes": ro([]string{}),
			"SupportedMimeTypes":  ro([]string{}),
		},
		playIface: {
			"PlaybackStatus": ro(s.status()),
			"LoopStatus":     {Value: loopName(s.c.Repeat()), Writable: true, Emit: prop.EmitTrue, Callback: s.setLoop},
			"Shuffle":        {Value: s.c.Shuffled(), Writable: true, Emit: prop.EmitTrue, Callback: s.setShuffle},
			"Volume":         {Value: s.c.Volume(), Writable: true, Emit: prop.EmitTrue, Callback: s.setVolume},
			"Metadata":       ro(s.metadata()),
			// Position is deliberately NOT emitted on change. The spec has
			// clients poll it and listen for Seeked, because a property signal
			// per second per player is a bus nobody wants.
			"Position":      {Value: int64(0), Emit: prop.EmitFalse},
			"Rate":          ro(1.0),
			"MinimumRate":   ro(1.0),
			"MaximumRate":   ro(1.0),
			"CanGoNext":     ro(true),
			"CanGoPrevious": ro(true),
			"CanPlay":       ro(true),
			"CanPause":      ro(true),
			"CanSeek":       ro(true),
			"CanControl":    ro(true),
		},
	}
}

// Update recomputes everything the bus can see and emits the changes.
//
// It is called from the player's own change hook, on the player's goroutine, and
// it is the ONLY writer — which is what makes "the bus is a view" true rather
// than aspirational.
func (s *Service) Update() {
	if s == nil || s.props == nil {
		return
	}
	s.props.SetMust(playIface, "PlaybackStatus", s.status())
	s.props.SetMust(playIface, "LoopStatus", loopName(s.c.Repeat()))
	s.props.SetMust(playIface, "Shuffle", s.c.Shuffled())
	s.props.SetMust(playIface, "Volume", s.c.Volume())
	s.props.SetMust(playIface, "Metadata", s.metadata())
	s.Tick()
}

// Tick refreshes Position alone, and is called on the UI's own clock.
//
// Position needs a different treatment from every other property because the
// D-Bus properties helper stores values rather than computing them: there is no
// getter hook, so a property that changes continuously has to be written. It is
// declared EmitFalse, so writing it stores the number and sends NOTHING — which
// is exactly what the spec asks for. Clients poll Position and listen for
// Seeked; a PropertiesChanged five times a second per player is a bus nobody
// wants.
func (s *Service) Tick() {
	if s == nil || s.props == nil {
		return
	}
	elapsed, _ := s.c.Position()
	s.props.SetMust(playIface, "Position", micros(elapsed))
}

// status is the three-valued PlaybackStatus.
//
// Two distinctions matter and neither is visible on screen. A track being
// DOWNLOADED reports Playing: the person pressed play, the widget should say so,
// and "Stopped" would make the desktop look like it had lost the command. And
// Paused means a track is open and held — a queue with a current item that
// nothing has loaded is Stopped, which is what the bus's Stop leaves behind.
func (s *Service) status() string {
	switch {
	case s.c.Playing() || s.c.Loading():
		return "Playing"
	case s.c.Paused():
		return "Paused"
	}
	return "Stopped"
}

// metadata is the xesam/mpris description of the current track.
//
// It is built from the QUEUE ITEM's captured text rather than from a library
// row, which is what makes it right for a track whose drive was unplugged after
// it was queued — the same reason the queue captures that text at all.
func (s *Service) metadata() map[string]dbus.Variant {
	cur := s.c.Current()
	if cur == nil {
		// An empty map, not a nil one: clients read the signature.
		return map[string]dbus.Variant{}
	}

	m := map[string]dbus.Variant{
		"mpris:trackid": dbus.MakeVariant(s.trackID(cur)),
		"xesam:title":   dbus.MakeVariant(cur.Title),
	}
	if cur.Artist != "" {
		// xesam:artist is a LIST. A single string here is the most common
		// MPRIS bug and it makes clients show nothing at all.
		m["xesam:artist"] = dbus.MakeVariant([]string{cur.Artist})
	}
	if cur.Album != "" {
		m["xesam:album"] = dbus.MakeVariant(cur.Album)
	}
	if _, total := s.c.Position(); total > 0 {
		m["mpris:length"] = dbus.MakeVariant(micros(total))
	} else if cur.Duration > 0 {
		m["mpris:length"] = dbus.MakeVariant(micros(cur.Duration))
	}
	if u := fileURL(s.c.ArtPath()); u != "" {
		m["mpris:artUrl"] = dbus.MakeVariant(u)
	}
	return m
}

// fileURL turns a path into the file:// URL the bus wants, or "" for no path.
//
// url.URL rather than "file://"+path, because a cover lives beside the music and
// music directories are full of spaces, "#" and every other character that has
// to be escaped before a URL parser will accept it.
func fileURL(path string) string {
	if path == "" {
		return ""
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return ""
	}
	return (&url.URL{Scheme: "file", Path: abs}).String()
}

// trackID is the object path the bus identifies the current track by.
//
// The queue has no such notion, so one is minted here and kept while the track
// is: a client that sees the id change knows the track changed, and one that
// sees it stay knows a metadata edit is about the same song. It must be a valid
// object path, which is why it is a counter and not the title.
func (s *Service) trackID(cur *queue.Item) dbus.ObjectPath {
	key := cur.RowKey()
	s.mu.Lock()
	defer s.mu.Unlock()
	if key != s.lastKey {
		s.lastKey = key
		s.trackNo++
	}
	return dbus.ObjectPath(fmt.Sprintf("%s/Track/%d", objPath, s.trackNo))
}

// The three writable properties all hand their work to a goroutine, and that is
// NOT tidiness — it is the fix for a deadlock that hangs every client.
//
// The properties helper holds its own lock across the callback. Changing the
// player from inside one reaches the player's change hook, which calls Update,
// which writes properties, which takes that same lock — and a sync.RWMutex is
// not reentrant, so the call never returns and `playerctl shuffle On` sits there
// until it times out. Dispatching also happens to be the right shape on its own:
// a bus method has no business blocking on a decoder.

func (s *Service) setVolume(c *prop.Change) *dbus.Error {
	v, ok := c.Value.(float64)
	if !ok {
		return dbus.MakeFailedError(errors.New("Volume must be a double"))
	}
	go s.c.SetVolume(v)
	return nil
}

func (s *Service) setShuffle(c *prop.Change) *dbus.Error {
	on, ok := c.Value.(bool)
	if !ok {
		return dbus.MakeFailedError(errors.New("Shuffle must be a boolean"))
	}
	go s.c.SetShuffle(on)
	return nil
}

func (s *Service) setLoop(c *prop.Change) *dbus.Error {
	name, ok := c.Value.(string)
	if !ok {
		return dbus.MakeFailedError(errors.New("LoopStatus must be a string"))
	}
	r, ok := loopValue(name)
	if !ok {
		return dbus.MakeFailedError(errors.New("unknown LoopStatus " + name))
	}
	go s.c.SetRepeat(r)
	return nil
}

// loopName and loopValue map this player's repeat modes onto MPRIS's names.
// They are the same three states under different words.
func loopName(r queue.Repeat) string {
	switch r {
	case queue.RepeatAll:
		return "Playlist"
	case queue.RepeatOne:
		return "Track"
	}
	return "None"
}

func loopValue(name string) (queue.Repeat, bool) {
	switch name {
	case "None":
		return queue.RepeatOff, true
	case "Playlist":
		return queue.RepeatAll, true
	case "Track":
		return queue.RepeatOne, true
	}
	return queue.RepeatOff, false
}

// micros converts seconds to the microseconds MPRIS counts in, saturating
// rather than wrapping: a nonsense length from a broken decoder must not come
// out the other side as a negative one.
func micros(seconds float64) int64 {
	v := seconds * 1e6
	if math.IsNaN(v) || v < 0 {
		return 0
	}
	if v > float64(math.MaxInt64) {
		return math.MaxInt64
	}
	return int64(v)
}

// --- methods ----------------------------------------------------------------

// rootHandler is org.mpris.MediaPlayer2.
type rootHandler struct{ s *Service }

func (h rootHandler) Raise() *dbus.Error {
	// CanRaise is false and this is exported anyway: the spec says the method
	// exists either way, and a client that calls it regardless gets a polite
	// no-op rather than an unknown-method error.
	return nil
}

func (h rootHandler) Quit() *dbus.Error {
	h.s.quitOnce.Do(func() {
		if h.s.onQuit != nil {
			go h.s.onQuit()
		}
	})
	return nil
}

// playerHandler is org.mpris.MediaPlayer2.Player.
type playerHandler struct{ s *Service }

// playerMethodNames renames the one Go method that cannot carry its bus name.
// Everything else on this interface is spelled the same on both sides.
var playerMethodNames = map[string]string{"SeekBy": "Seek"}

// playerMethods is the introspection data with that rename applied — a client
// reads the method list before calling anything, so a name that disagreed with
// the export would advertise a method nobody can invoke.
func playerMethods() []introspect.Method {
	ms := introspect.Methods(playerHandler{})
	for i := range ms {
		if bus, renamed := playerMethodNames[ms[i].Name]; renamed {
			ms[i].Name = bus
		}
	}
	return ms
}

func (h playerHandler) Next() *dbus.Error     { h.s.c.Next(); return nil }
func (h playerHandler) Previous() *dbus.Error { h.s.c.Prev(); return nil }
func (h playerHandler) Play() *dbus.Error     { h.s.c.Play(); return nil }
func (h playerHandler) Pause() *dbus.Error    { h.s.c.Pause(); return nil }
func (h playerHandler) PlayPause() *dbus.Error {
	h.s.c.Toggle()
	return nil
}
func (h playerHandler) Stop() *dbus.Error { h.s.c.Stop(); return nil }

// SeekBy is the bus's Seek: RELATIVE, in microseconds, and may be negative.
//
// The Go method is not called Seek because `go vet`'s stdmethods check reads
// any method of that name as a botched io.Seeker. The bus name is restored by
// playerMethodNames, which is also why the introspection data is patched rather
// than taken straight from reflection.
func (h playerHandler) SeekBy(offset int64) *dbus.Error {
	elapsed, total := h.s.c.Position()
	if total <= 0 {
		return nil
	}
	h.s.seekTo(elapsed + float64(offset)/1e6)
	return nil
}

// SetPosition is absolute, and carries the track it believes is playing. The id
// is checked rather than ignored: a widget can send a position for the track
// that was showing a moment ago, and honouring it would seek the WRONG song.
func (h playerHandler) SetPosition(track dbus.ObjectPath, position int64) *dbus.Error {
	cur := h.s.c.Current()
	if cur == nil || track != h.s.trackID(cur) {
		return nil
	}
	h.s.seekTo(float64(position) / 1e6)
	return nil
}

// OpenUri is not supported: this player plays its own library, and there is no
// sense in which a desktop can hand it a file to play (that is what the folder
// scanner is). Saying so is better than pretending.
func (h playerHandler) OpenUri(uri string) *dbus.Error {
	return dbus.MakeFailedError(errors.New("madplayer plays its own library; add a folder in Settings"))
}

// seekTo moves the playhead and tells the bus, which is the one thing a client
// cannot work out for itself: Position is not signalled, so a seek that emitted
// nothing would leave every widget's progress bar wrong until it next polled.
func (s *Service) seekTo(seconds float64) {
	if seconds < 0 {
		seconds = 0
	}
	s.c.Seek(seconds)
	if s.conn == nil {
		return
	}
	elapsed, _ := s.c.Position()
	_ = s.conn.Emit(objPath, playIface+".Seeked", micros(elapsed))
}
