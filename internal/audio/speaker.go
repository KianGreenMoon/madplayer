//go:build !android

// Package audio is the real audio device — the one place in madplayer that
// needs a sound card, and therefore cgo (ALSA on Linux, CoreAudio on macOS,
// WASAPI on Windows) by way of beep's speaker and oto. Android bypasses
// beep's speaker — see speaker_android.go for why its buffer split can never
// feed oto's driver there.
//
// It is a package of its own precisely so nothing else has to be. Everything
// that decides anything — decode, seek, position, queue advance, the whole UI —
// lives behind player.Sink and builds and tests on a machine with no audio at
// all.
package audio

import (
	"sync"
	"time"

	"github.com/gopxl/beep/v2"
	"github.com/gopxl/beep/v2/speaker"
)

// Speaker adapts beep's process-global speaker to player.Sink.
type Speaker struct {
	once   sync.Once
	inited bool
}

// New returns an unopened speaker.
func New() *Speaker { return &Speaker{} }

// Init opens the device at the requested rate, which is also the answer:
// ALSA's plug layer takes any rate, and the desktop mixers behind it (Pipe-
// Wire, PulseAudio) resample well — there is no native rate worth chasing
// here the way there is on Android.
//
// beep's speaker is a process-global that panics if initialised twice, so this
// is guarded — a second player in the same process (a test, a restart after a
// device change) must not take the program down.
func (s *Speaker) Init(rate beep.SampleRate, bufferSize int) (beep.SampleRate, error) {
	var err error
	s.once.Do(func() {
		err = speaker.Init(rate, bufferSize)
		s.inited = err == nil
	})
	return rate, err
}

func (s *Speaker) Play(st beep.Streamer) {
	if s.inited {
		speaker.Play(st)
	}
}

func (s *Speaker) Lock() {
	if s.inited {
		speaker.Lock()
	}
}

func (s *Speaker) Unlock() {
	if s.inited {
		speaker.Unlock()
	}
}

// SetPaused does nothing here, and the Android sink explains why it exists:
// there, a pause that only stops the mixer is still heard for as long as the
// pool is deep, so the device is held instead. beep's speaker offers no such
// hold — and needs none. Its buffer is the one the player asks for, tens of
// milliseconds, and the desktop mixers below it (PipeWire, PulseAudio) are not
// fed a quarter of a second ahead.
func (s *Speaker) SetPaused(bool) {}

func (s *Speaker) Clear() {
	if s.inited {
		speaker.Clear()
	}
}

// Close releases the device.
func (s *Speaker) Close() error {
	if !s.inited {
		return nil
	}
	speaker.Clear()
	// Give the device a moment to drain before closing, or the last fraction of
	// a second is cut off with an audible click on exit.
	time.Sleep(20 * time.Millisecond)
	speaker.Close() // returns nothing; the device is process-global
	return nil
}
