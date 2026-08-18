# Local oto fork — madplayer patch

A vendored copy of [`github.com/ebitengine/oto/v3`](https://github.com/ebitengine/oto)
at **v3.4.0**, wired in by a `replace` in the repository-root `go.mod`. It
carries **two local changes** — one in `internal/oboe/binding_android.cpp`
(the performance mode, below) and one in `internal/mux/mux.go` + `player.go`
(pool statistics, below). Grep for `LOCAL PATCH (madplayer)`; both are marked.
If you bump oto, re-apply both — or drop the first the day oto lets an embedder
choose the performance mode, and the second the day it reports a short buffer.

(v3.4.1 changes nothing on this path: `driver_android.go` and
`internal/oboe/binding_android.cpp` are byte-identical between the two.)

## The patch — ask for the deep buffer, not the low-latency path

Upstream opens its Oboe stream with `PerformanceMode::LowLatency`,
unconditionally and with no way to override it from Go. AAudio is compiled out
(`-DOBOE_ENABLE_AAUDIO=0`, upstream's own choice — AAudio does not handle
headphones being unplugged), so that request goes through OpenSL ES, where
`SL_ANDROID_PERFORMANCE_LATENCY` becomes
`AUDIO_OUTPUT_FLAG_FAST|AUDIO_OUTPUT_FLAG_RAW`: a FAST mixer track on an output
stream whose HAL buffer is **128 frames — 2.7 ms**, and which bypasses the
device's output post-processing.

That is the wrong path for a music player, and on a Pixel 7 Pro it is audibly
the wrong path. Measured 2026-08-18 over one 60-second window per row, same
track, same speaker, `dumpsys media.audio_flinger`:

| stream | who | HAL buffer | HAL FIFO underruns |
|---|---|---|---|
| `FAST\|RAW` | madplayer through upstream oto | 128 frames (2.7 ms) | **40 / min**, ~72 frames each |
| `DEEP_BUFFER` | the phone's browser, same file | 960 frames (20 ms) | **0** |

~40 gaps of ~1.5 ms per minute is a click every second and a half. It is what
the owner had been reporting as "crackle" since 2026-08-16, through four
rounds of fixes further up the chain that could not have helped.

**It is not the app being late.** Over that same window AudioFlinger's fast
mixer found madplayer's track FULL on every cycle — the `Empty` and `Partial`
counters in its fast-track table did not move at all — and the stream still
underran 15 times a minute while the app was PAUSED and feeding silence. The
2.7 ms buffer is below what this device's HAL sustains, whoever fills it.

`PerformanceMode::None` maps to `SL_ANDROID_PERFORMANCE_NONE` and lands on
`AUDIO_OUTPUT_FLAG_DEEP_BUFFER` — the stream the browser uses, the one that
measured zero. It also stops bypassing the output post-processing every other
app on the device goes through.

The price is output latency: roughly 20 ms of HAL buffer instead of 2.7 ms,
plus oboe's own queue. A synthesiser would care. A player whose every control
goes through a mixer it clears does not — and `audio.Speaker.Clear` already
drops oto's Go-side pool, which is the larger part of what a listener hears as
lag after pressing pause.

## Upstreaming

Worth offering: an option on `oto.NewContextOptions` for the performance mode,
defaulting to today's behaviour. Every media app embedding oto on Android has
this problem and no way to see it — nothing in the Go layer can observe a HAL
FIFO underrun, which is why it took a `dumpsys` A/B against a browser to find.

## The second patch — count what the mux does not report

`mux.ReadFloat32s` zero-fills whatever a player cannot supply
(`readBufferAndAdd` adds only what is buffered), so a buffer that is short when
the device reads becomes **silence in the mix**: no error, no short read, no
counter. Nothing above this package can see it, and on Android the device's
read is 3× the granted stream buffer — 120 ms on the phone above — against a
Go-side buffer madplayer sizes itself. Choosing that size was guesswork with no
feedback.

`PoolStats` (`mux.playerImpl` → `oto.Player.TakePoolStats`) is that feedback,
collected under the lock the read already holds:

| field | meaning |
|---|---|
| `Reads` | device-side reads served in the window |
| `MaxRead` | bytes wanted by the largest of them — the device's read size, which the Go side otherwise cannot know |
| `LowWater` | fewest bytes left after a read — **the margin**, the number that says how close it came even when it never went over |
| `Shorts` / `ShortBytes` | reads the buffer could not fill, and how much silence went out instead |

madplayer prints them on its 30 s `audio: stats` line, which is what makes a
crackle report carry its own evidence.

Two tests in `internal/mux/madplayer_pool_test.go` pin it and the sizing rule
it exists to serve, measured rather than reasoned:

- a short buffer is mixed as silence and counted;
- **the pool is the only thing that buys time for a late refill.** A player is
  refilled only when its buffer falls BELOW `bufferSize`, and is then topped up
  by a whole one — so the level sawtooths between B and 2B, the worst phase to
  stall in leaves B−X, and the refill goroutine can be late by:

  | pool | device read | survives |
  |---|---|---|
  | 250 ms | 120 ms | 240 ms |
  | 250 ms | 40 ms | 240 ms |
  | 500 ms | 120 ms | 480 ms |
  | 500 ms | 40 ms | 480 ms |

  Doubling the pool doubles the time. Shrinking the device's read does **not**
  change it — it only makes the hole smaller when the time runs out (110 ms of
  silence at a 120 ms read, 30 ms at a 40 ms one). That is why madplayer's
  lever is `poolDuration` and not `driverBuffer`, and why raising the driver
  request without raising the pool makes things worse: the device's read grows
  with it, and a read bigger than half the pool cannot be satisfied at all.

### Upstreaming this one

Also worth offering, in a smaller way than the performance mode: an embedder
that must choose `SetBufferSize` has no way to know whether the choice worked.
`BufferedSize` samples a level that swings by design; the low-water mark and
the short count are what the choice is actually about.
