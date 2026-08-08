# madplayer

A native music player in Go, for desktop and mobile. Design and rationale live
in [`../docs/ui/madplayer.md`](../docs/ui/madplayer.md); this file covers how to
work on the code.

**It is an offline player first.** Point it at your music folders and it scans,
indexes and plays them — no server, no account, no network, nothing to sign in
to. Reaching a madshare server is a feature layered on top of that, never a
precondition.

**Status:** local player built (scan, index, browse, search, queue, playback).
The madshare client is written but not yet wired to a screen.

## What works

- **Folder scanning**, in place. Nothing is copied, moved or written into the
  scanned tree — the same hard invariant the server's import-in-place data
  sources carry. A rescan skips files whose size and mtime are unchanged.
- **Browse**: artists → albums → tracks, over resolved entities rather than raw
  tags, with search across both artist roles.
- **Playback**: MP3, FLAC, WAV and Ogg Vorbis, with a queue, shuffle, three-mode
  repeat, seeking and volume.
- **Durations** are measured by the decoder in the background, so the list
  appears immediately with `—` instead of waiting on a walk over every file.

M4A/AAC/Opus files **are indexed but cannot be played**: they need cgo bindings
or ffmpeg, which is listed under the native client's own burdens in the design
doc. Showing such a track and saying it cannot be played is honest; hiding it
would look like the file is missing.

## Its own Go module, on purpose

`madplayer/` is a **separate module** (`daemonlord.ygg/madplayer`), not part of
the root `daemonlord.ygg/madshare` one. A GUI toolkit's `require` and `go.sum`
lines in the server's `go.mod` would be client code arriving in the server by the
back door — which is the one thing this branch exists to prevent.

Consequences:

- The repo root's `go build ./...` / `go test ./...` do **not** cover madplayer.
  Build and test it from this directory.
- Embedding the backend in-process later adds `require daemonlord.ygg/madshare`
  plus a `replace ... => ../` **here**, never the other way round. The dependency
  points client → server, always.

## Branch discipline

This lives on the temporary `madplayer` branch and moves to its own repo later,
so the split stays a `git subtree split -P madplayer`:

- **No commit may touch `madplayer/` and anything outside it.** A commit is
  either client or server, never both. That is what lets a server-side fix reach
  the main branch on its own.
- Server changes are made on `aidev` and merged **forward** into `madplayer`.
  `madplayer` is never merged back.
- The target is **zero** server changes.
- `../docs/ui/*` is the cross-client contract, so editing one of those docs is a
  server commit. Client-only notes belong in this file.

## Layout

```
cmd/madplayer/       the program
internal/library/    scan, entity resolution, browse, search — no I/O beyond reading files
internal/queue/      play queue: index arithmetic, shuffle, repeat
internal/player/     decode, position, seek, queue advance — no audio device
internal/audio/      the audio device (cgo/ALSA). The ONLY package that needs one
internal/madshare/   HTTP client for a madshare server — for the connected half, not yet wired
android/             APK packaging (x86_64 build host only — see below)
```

The layering has one rule worth keeping: **`internal/player` does not import an
audio device.** The output is injected as a `player.Sink`, so every decision —
decode, seek, position, repeat, error recovery — is tested with a silent test
double, and the one package that needs a sound card sits at the edge of the
program. `internal/audio` is the whole of the cgo surface.

## The rules that are ports, not inventions

`internal/library/identity.go` and `internal/queue` are **ports** of code that
already exists on the server side, and they say so in their doc comments:

| Here | Ported from | Contract |
|---|---|---|
| `library.EffectiveArtist` etc. | `database/entities.go` | `docs/architecture/artist-album-model.md` |
| `library.Index` browse rules | `database/library.go` | `docs/ui/artists-and-performers.md` |
| `queue` index arithmetic | `webui/static/js/queue-ops.js` | `docs/ui/player-and-queue.md` |
| disc grouping | `webui/static/js/disc.js` | `docs/architecture/disc-numbering.md` |

They are duplicated because madplayer must run with no server at all — but the
same folder scanned here and uploaded there has to produce the same artists, the
same albums and the same buckets. `internal/library/index_test.go` pins this
against the worked example in `artists-and-performers.md`, and
`internal/queue/queue_test.go` mirrors `tests/js/queue-ops.test.mjs` case for
case. If one of those needs different expectations from its twin, the contract
has changed and the doc moves in the same commit.

## Build prerequisites (Linux)

```bash
sudo dnf install -y alsa-lib-devel libxkbcommon-x11-devel vulkan-headers
```

- `alsa-lib-devel` — **required**: the audio output (oto) will not build without
  it. PipeWire's ALSA compatibility handles the rest at runtime.
- `libxkbcommon-x11-devel` — Gio's X11 backend; without it build `-tags nox11`
  (Wayland only).
- `vulkan-headers` — Gio's Vulkan backend; without it build `-tags novulkan`
  (falls back to OpenGL ES).

Gio is **native Wayland** on a Wayland session — it compiles both backends in and
picks at runtime.

## Running

```bash
go run ./cmd/madplayer
go test ./internal/...
```

On first start it opens on **Folders**: type or paste a path and press *Add
folder*. There is no native folder picker in Gio, so the path is validated
before it is accepted — a silently-ignored typo looks exactly like an empty
library.

State lives in `~/.config/madplayer/` (`config.json`, `library.json`). Deleting
it loses nothing but the scan cache.

## Android

**The APK cannot be built on an aarch64 Linux host.** `gogio` resolves the NDK
through an `archNDK()` that ends in `panic("unsupported GOARCH: arm64")`, the NDK
ships no linux-aarch64 host toolchain, and the 16 KB-page emulation wall from
`../docs/architecture/android-app.md` sits behind both. `android/build-apk.sh`
builds on an x86_64 host and refuses loudly elsewhere; `adb` runs natively on
aarch64, so install-and-look still happens on the development machine.
