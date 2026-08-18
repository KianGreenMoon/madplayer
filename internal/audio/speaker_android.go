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
//
// That fixed the noise and left a crackle, which was NOT ours: oto asks
// Android for the low-latency output, whose HAL buffer on this phone is
// 2.7 ms and drops ~1.5 ms of audio some forty times a minute no matter who
// fills it. oto is forked for the one line that asks
// (third_party/oto/MADPLAYER-PATCH.md); the measurement that found it is
// there too, and it is worth reading before touching any size in this file,
// because four rounds of tuning up the chain could not have helped.
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
	// driverBuffer is the capacity asked of the device, and since 2026-08-18
	// it is asked of the DEEP BUFFER path: oto is forked for one line so it
	// stops requesting Android's low-latency output
	// (third_party/oto/MADPLAYER-PATCH.md). That matters more than the number
	// here. Measured on the Pixel 7 Pro, 60 s windows, same track, same
	// speaker: the FAST|RAW stream upstream oto asks for has a 128-frame
	// (2.7 ms) HAL buffer and underran 40 times a minute, ~72 frames each —
	// a click every second and a half — while the browser's DEEP_BUFFER
	// stream, 960 frames, underran zero times. Not our lateness: AudioFlinger
	// found this app's track full on every mixer cycle throughout, and the
	// stream still underran while the app was paused and feeding silence.
	//
	// On that path the device's burst is 20 ms, and oboe rounds a request up
	// to whole bursts — so 25 ms buys the double buffering that is the norm
	// there, and oto's own fifo is 3× the granted capacity on top.
	driverBuffer = 25 * time.Millisecond

	// poolDuration sizes the Go-side buffer those reads drain, and it is the
	// ONLY thing standing between a late refill and an audible hole. Measured,
	// not reasoned (third_party/oto/internal/mux/madplayer_pool_test.go, which
	// drives the real mux): oto refills a player only when its buffer falls
	// BELOW this and then tops it up by a whole one, so the level sawtooths
	// between one and two of them and the worst moment to stall in leaves
	// poolDuration minus one device read. A refill that lands later than that
	// is answered with silence.
	//
	//   pool 250 ms → a refill may be 240 ms late; a 120 ms read then holes
	//   pool 500 ms → 480 ms
	//
	// The size of the device's read does not change that time — only how big
	// the hole is when it runs out — so raising driverBuffer alone makes this
	// WORSE, because the read is three times the grant. 500 ms is here
	// because the phone's refill goroutine is visibly jittery: 60 ms of slop
	// on the read cadence in an ordinary window, GC pauses of tens of ms, and
	// an OS that freezes the whole process when it feels like it. Whether
	// 240 ms was ever actually breached is now a question the log answers —
	// the "pool low" figure on the stats line is the margin, and PADDED is it
	// running out.
	//
	// The price a listener would pay for the depth is paid elsewhere instead:
	// a pause holds the device (SetPaused) rather than waiting the pool out,
	// and a scrub drops it (Flush).
	poolDuration = 500 * time.Millisecond

	// poolCapacity is what oto REALLY holds, and it is not poolDuration.
	// Its mux refills a player when the buffer falls BELOW bufferSize, and
	// the refill reads a whole bufferSize and appends it (mux.go
	// canReadSourceToBuffer + readSourceToBuffer), so the level swings
	// between one and nearly two of them. The starvation model below has to
	// use this figure or it reports a dry pool while a whole poolDuration of
	// audio is still queued — a false alarm on a healthy build, which is
	// worse than no alarm at all.
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
	reader *streamReader
	rate   beep.SampleRate
	inited bool
	stats  *streamStats

	// pmu guards the pair (paused, the oto player's own state), which two
	// callers can reach at once: the UI pausing, and a track change clearing.
	// They must not interleave, or a cleared sink restarts a player the user
	// paused — which plays.
	pmu    sync.Mutex
	paused bool
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
		s.reader = &streamReader{
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
		}
		s.player = ctx.NewPlayer(s.reader)
		s.player.SetBufferSize(rate.N(poolDuration) * frameBytes)
		s.player.Play()
		go logStats(stats, s.player, rate)
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
func logStats(st *streamStats, pl *oto.Player, rate beep.SampleRate) {
	const period = 30 * time.Second
	// The pool is polled far more often than the line is printed, because a
	// short read is an audible click and a 30 s window would only say that one
	// happened somewhere in it. Polling puts a timestamp on it, which is what
	// makes "I heard it at 20:14" answerable.
	const poll = time.Second
	var m runtime.MemStats
	var lastGC uint32
	var lastPause uint64
	prev := time.Now()
	var lag time.Duration // audio owed to the wall clock since the last reset
	var pool oto.PoolStats
	lowWater := -1 // bytes; -1 until a device read has happened
	audioTime := func(bytes int) time.Duration { return rate.D(bytes / frameBytes) }
	for {
		time.Sleep(poll)
		if p := pl.TakePoolStats(); p.Reads > 0 {
			pool.Reads += p.Reads
			pool.Shorts += p.Shorts
			pool.ShortBytes += p.ShortBytes
			pool.MaxRead = max(pool.MaxRead, p.MaxRead)
			if lowWater < 0 || p.LowWater < lowWater {
				lowWater = p.LowWater
			}
			if p.Shorts > 0 {
				// The one failure oto cannot report: the device asked for more
				// than the pool held, and the difference went out as silence.
				// Nothing else in this process can see it — not the reads, not
				// the gaps, not AudioFlinger, which counts the track as fed.
				//
				// Read it against the timestamps either side: one landing in
				// the same breath as a track change or a scrub is the pool
				// being dropped on purpose (Flush), and the boundary is silent
				// anyway. One in the middle of a track is the bug.
				log.Printf("audio: the device read past the pool — %v of silence mixed into %d read(s) of up to %v; the refill was late",
					audioTime(p.ShortBytes).Round(time.Millisecond), p.Shorts,
					audioTime(p.MaxRead).Round(time.Millisecond))
			}
		}
		if time.Since(prev) < period {
			continue
		}
		reads, worstExec, worstGap, starved, audio := st.take()
		interval := time.Since(prev)
		prev = time.Now()
		reset := st.restarted.Swap(false)
		if reads == 0 {
			lag = 0 // idle time is not lag; start the accounting over
			pool, lowWater = oto.PoolStats{}, -1
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
		// The pool's own numbers, from inside oto, and they are meant to be
		// read against each other: the device's read is what has to FIT in
		// what is left, so a low-water mark drifting down towards the read
		// size is the crackle coming, and one below it is the crackle. The
		// read size is knowable no other way — it is 3× a grant this side is
		// never told.
		if pool.Reads > 0 {
			line += fmt.Sprintf("; pool low %v against %d device read(s) of up to %v",
				audioTime(lowWater).Round(time.Millisecond),
				pool.Reads, audioTime(pool.MaxRead).Round(time.Millisecond))
		}
		if starved > 0 {
			line += fmt.Sprintf(" — STARVED %d×", starved)
		}
		if pool.Shorts > 0 {
			line += fmt.Sprintf(" — PADDED %d× (%v of silence)", pool.Shorts,
				audioTime(pool.ShortBytes).Round(time.Millisecond))
		}
		log.Print(line)
		pool, lowWater = oto.PoolStats{}, -1
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

// SetPaused holds the DEVICE rather than feeding it silence.
//
// A mixer pause (beep.Ctrl) stops the music at the top of the chain, and
// everything already handed over plays first: the pool, oto's C++ fifo, the
// HAL's own buffer. That tail is what a deep pool costs a listener — press
// pause, keep hearing music — and it is why the pool could not simply be made
// deeper. Pausing the PLAYER stops the pull instead, so the tail is only what
// lies below this process (about 0.2 s here) whatever the pool holds.
//
// The pooled audio is kept, not dropped: a resume continues exactly where it
// stopped, with nothing re-decoded and no gap. Only the reader's starvation
// model has to be told, or the stopped clock reads as a dry pool.
func (s *Speaker) SetPaused(paused bool) {
	if !s.inited {
		return
	}
	s.pmu.Lock()
	defer s.pmu.Unlock()
	s.paused = paused
	if paused {
		s.player.Pause()
		return
	}
	s.reader.restart()
	s.player.Play()
}

// Flush drops the pool — half a second of audio that was handed over and will
// never be heard. Reset also pauses the oto player, so it is started again in
// the same breath, unless the user is holding it paused: starting it there
// would play the next track under a Play button.
func (s *Speaker) Flush() {
	if !s.inited {
		return
	}
	s.pmu.Lock()
	s.player.Reset()
	if !s.paused {
		s.player.Play()
	}
	s.pmu.Unlock()
	// The two models that measure this feed both just lost their footing. The
	// reader's pool model would count the dropped audio as still buffered;
	// the lag accounting would count it as playing time that never played,
	// which is exactly the shape of a dropout below us. Say so instead.
	s.reader.restart()
	if s.stats != nil {
		s.stats.restarted.Store(true)
	}
}

// Clear stops everything currently playing, including what is already mixed
// into the pool — a track change should not play half a second of the old
// audio first.
func (s *Speaker) Clear() {
	if !s.inited {
		return
	}
	s.mu.Lock()
	s.mixer.Clear()
	s.mu.Unlock()
	s.Flush()
}

// Close releases the player. The oto context has no Close and outlives it,
// which on Android is the OS's problem, not ours: the process is going away.
func (s *Speaker) Close() error {
	if !s.inited {
		return nil
	}
	return s.player.Close()
}
