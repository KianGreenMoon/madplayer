# Local oto fork — madplayer patch

A vendored copy of [`github.com/ebitengine/oto/v3`](https://github.com/ebitengine/oto)
at **v3.4.0**, wired in by a `replace` in the repository-root `go.mod`. It
carries **one local change**, in `internal/oboe/binding_android.cpp`. Grep for
`LOCAL PATCH (madplayer)`. If you bump oto, re-apply it — or drop the fork the
day oto lets an embedder choose the performance mode.

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
