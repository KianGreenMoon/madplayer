//go:build android

// Android does not get beep's speaker, and the reason is a pair of numbers
// that cannot be pulled apart from outside: speaker.Init splits its buffer
// argument 50/50 between the driver request and the Go-side pool oto reads
// from, while oto's Android driver pulls 3× the granted stream buffer per
// read into its own C++ fifo. A read can therefore NEVER be satisfied — the
// pool holds half a buffer, the read wants one and a half — and oto pads the
// difference with silence, every read, at any buffer size. Heard on a Pixel 7
// Pro as "very very noisy": AudioFlinger counted 40% of the track's mix
// cycles EMPTY while the CPU sat 96% idle (2026-08-17, dumpsys
// media.audio_flinger, fast track 1: 274 full / 180 empty).
//
// So this sink drives oto directly, which makes the two sizes independent:
// a small driver request, and a pool sized to cover the triple-read with
// margin. Same beep.Mixer semantics as the desktop speaker, same Sink
// surface, float32 straight through instead of beep's int16 detour.
package audio

import (
	"sync"
	"time"

	"github.com/ebitengine/oto/v3"
	"github.com/gopxl/beep/v2"
)

const (
	// driverBuffer is the capacity asked of the device. The phone does not
	// have to honor it (this one granted a 1024-frame FAST track against a
	// 2205-frame request), but oto's read size is 3× whatever WAS granted,
	// so asking small keeps the reads small too.
	driverBuffer = 25 * time.Millisecond

	// poolDuration sizes the Go-side buffer those reads drain. It must
	// exceed 3× the granted stream buffer or the silence-padding above
	// comes back, and the grant is the device's call — hence a generous
	// margin over 3× the request rather than a tight one. The price is
	// heard on pause: a pause mixes silence from that moment on, but what
	// is already pooled still plays, up to this long. Clear() does not pay
	// it — it drops the pool.
	poolDuration = 250 * time.Millisecond
)

// Speaker adapts oto's Android device to player.Sink.
type Speaker struct {
	once   sync.Once
	mu     sync.Mutex
	mixer  beep.Mixer
	player *oto.Player
	inited bool
}

// New returns an unopened speaker.
func New() *Speaker { return &Speaker{} }

// Init opens the device. oto's context is a process-global that cannot be
// created twice, so this is guarded like the desktop speaker's.
//
// The bufferSize argument is ignored: it was sized for ALSA, and on Android
// the sizes that matter are the two above.
func (s *Speaker) Init(rate beep.SampleRate, _ int) error {
	var err error
	s.once.Do(func() {
		ctx, ready, e := oto.NewContext(&oto.NewContextOptions{
			SampleRate:   int(rate),
			ChannelCount: 2,
			Format:       oto.FormatFloat32LE,
			BufferSize:   driverBuffer,
		})
		if e != nil {
			err = e
			return
		}
		<-ready
		s.player = ctx.NewPlayer(&streamReader{mu: &s.mu, src: &s.mixer})
		s.player.SetBufferSize(rate.N(poolDuration) * frameBytes)
		s.player.Play()
		s.inited = true
	})
	return err
}

func (s *Speaker) Play(st beep.Streamer) {
	if !s.inited {
		return
	}
	s.mu.Lock()
	s.mixer.Add(st)
	s.mu.Unlock()
}

func (s *Speaker) Lock() {
	if s.inited {
		s.mu.Lock()
	}
}

func (s *Speaker) Unlock() {
	if s.inited {
		s.mu.Unlock()
	}
}

// Clear stops everything currently playing, including what is already mixed
// into the pool — a track change or a seek should not play a quarter second
// of the old audio first. Reset also pauses the oto player, so it is started
// again in the same breath.
func (s *Speaker) Clear() {
	if !s.inited {
		return
	}
	s.mu.Lock()
	s.mixer.Clear()
	s.mu.Unlock()
	s.player.Reset()
	s.player.Play()
}

// Close releases the player. The oto context has no Close and outlives it,
// which on Android is the OS's problem, not ours: the process is going away.
func (s *Speaker) Close() error {
	if !s.inited {
		return nil
	}
	return s.player.Close()
}
