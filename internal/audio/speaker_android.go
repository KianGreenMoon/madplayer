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
	"fmt"
	"log"
	"runtime"
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

	// poolCapacity is what oto REALLY holds, and it is not poolDuration.
	// Its mux refills a player when the buffer falls BELOW bufferSize, and
	// the refill reads a whole bufferSize and appends it (mux.go
	// canReadSourceToBuffer + readSourceToBuffer), so the level swings
	// between one and nearly two of them. The starvation model below has to
	// use this figure or it reports a dry pool while up to 250 ms of audio
	// is still queued — a false alarm on a healthy build, which is worse
	// than no alarm at all.
	poolCapacity = 2 * poolDuration

	// readDeadline is how long one Read may take before zeros are CERTAIN:
	// the refill fires with one bufferSize still buffered, so that is the
	// whole runway. Half of it is the warning line — a read that slow has no
	// margin left for the scheduler.
	readDeadline = poolDuration
)

// Speaker adapts oto's Android device to player.Sink.
type Speaker struct {
	once   sync.Once
	mu     sync.Mutex
	mixer  beep.Mixer
	player *oto.Player
	rate   beep.SampleRate
	inited bool
	stats  *streamStats
}

// New returns an unopened speaker.
func New() *Speaker { return &Speaker{} }

// Init opens the device — at the DEVICE's rate, not the requested one, when
// the two differ. oto never surfaces this, but its oboe layer opens the
// stream at the phone's native rate regardless and quietly converts whatever
// rate it was asked for with an 8-tap resampler (Medium quality, its
// default; measured on a Pixel 7 Pro 2026-08-18: sink fed 44.1 kHz, the
// AudioFlinger track ran at 48 kHz). Asking for the native rate up front
// makes that hidden stage a pass-through, and the player aims its own,
// better resampler at the true rate instead — the returned value is how it
// finds out.
//
// oto's context is a process-global that cannot be created twice, so this is
// guarded like the desktop speaker's.
//
// The bufferSize argument is ignored: it was sized for ALSA, and on Android
// the sizes that matter are the two above.
func (s *Speaker) Init(rate beep.SampleRate, _ int) (beep.SampleRate, error) {
	var err error
	s.once.Do(func() {
		if native := nativeOutputSampleRate(); native > 0 && native != int(rate) {
			log.Printf("audio: device rate %d Hz (requested %d) — opening native", native, rate)
			rate = beep.SampleRate(native)
		}
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
		s.rate = rate
		stats := &streamStats{}
		s.stats = stats
		s.player = ctx.NewPlayer(&streamReader{
			mu:   &s.mu,
			src:  &s.mixer,
			rate: rate,
			pool: poolCapacity,
			// The pool absorbs feed lateness up to its depth; past that oto
			// pads the mix with zeros — a gap the ear reads as crackle and
			// AudioFlinger's counters cannot see (the track still looks
			// fed). warn is slack for the driver's bursty triple-reads, so
			// what gets logged is real starvation, with its depth, as the
			// evidence a crackle report needs from logcat.
			warn: 3 * driverBuffer,
			late: func(gap time.Duration) {
				log.Printf("audio: feed starved ~%v past the %v pool — audible gap likely", gap.Round(time.Millisecond), poolCapacity)
			},
			// slow separates the two ways the pool can drain: this line means
			// the decode chain itself cannot keep realtime; starvation WITHOUT
			// it means the reads never came — a GC pause, the scheduler, or
			// Android freezing the process.
			slow: func(exec, audio time.Duration) {
				log.Printf("audio: slow pull — %v to produce %v of audio (the runway is %v); the chain in the pull is behind realtime",
					exec.Round(time.Millisecond), audio.Round(time.Millisecond), readDeadline)
			},
			stats: stats,
		})
		s.player.SetBufferSize(rate.N(poolDuration) * frameBytes)
		s.player.Play()
		go logStats(stats)
		s.inited = true
	})
	return s.rate, err
}

// logStats writes one line per interval while audio is being pulled, and
// nothing while it is not. It is the context the event lines above lack: a
// starved read means little without knowing whether GC was pausing, the heap
// was climbing, or the whole interval arrived late — the stamp on this line
// jumping from 30s to minutes is the process having been FROZEN by Android's
// cached-app freezer, which no in-process counter can record while it
// happens. The ring in logbuf keeps hours of these; logcat on the diagnosing
// phone kept ten minutes.
func logStats(st *streamStats) {
	const period = 30 * time.Second
	var m runtime.MemStats
	var lastGC uint32
	var lastPause uint64
	prev := time.Now()
	var lag time.Duration // audio owed to the wall clock since the last reset
	for {
		time.Sleep(period)
		reads, worstExec, worstGap, starved, audio := st.take()
		interval := time.Since(prev)
		prev = time.Now()
		reset := st.restarted.Swap(false)
		if reads == 0 {
			lag = 0 // idle time is not lag; start the accounting over
			continue
		}
		// The device's clock against ours. Anything below this program that
		// runs dry plays silence in its place, and the audio handed over
		// falls behind the wall clock by that much, for good — so a lag that
		// GROWS is a glitch nothing here can otherwise see. A track change or
		// a seek throws pooled audio away, which looks exactly the same, so
		// the accounting starts over whenever the sink is cleared.
		if reset {
			lag = 0
		} else {
			lag += interval - audio
		}
		runtime.ReadMemStats(&m)
		gc, pause := m.NumGC-lastGC, m.PauseTotalNs-lastPause
		lastGC, lastPause = m.NumGC, m.PauseTotalNs
		line := fmt.Sprintf("audio: stats %v: %d reads, worst pull %v, worst gap %v, %v of audio (lag %v); heap %dMB, %d gc (%v paused), %d goroutines",
			interval.Round(time.Second), reads,
			worstExec.Round(time.Millisecond), worstGap.Round(time.Millisecond),
			audio.Round(time.Millisecond), lag.Round(time.Millisecond),
			m.HeapAlloc>>20, gc, time.Duration(pause).Round(time.Millisecond), runtime.NumGoroutine())
		if starved > 0 {
			line += fmt.Sprintf(" — STARVED %d×", starved)
		}
		log.Print(line)
	}
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
	// Pooled audio just went in the bin: it was handed over and will never
	// play, which is indistinguishable from silence inserted below us. Say so
	// rather than let a track change read as a dropout.
	if s.stats != nil {
		s.stats.restarted.Store(true)
	}
}

// Close releases the player. The oto context has no Close and outlives it,
// which on Android is the OS's problem, not ours: the process is going away.
func (s *Speaker) Close() error {
	if !s.inited {
		return nil
	}
	return s.player.Close()
}
