# madplayer

A native music player in Go, for desktop and mobile. Design and rationale live
in [`../docs/ui/madplayer.md`](../docs/ui/madplayer.md); this file covers how to
work on the code.

**It is an offline player first.** Point it at your music folders and it scans,
indexes and plays them — no server, no account, no network, nothing to sign in
to. Reaching a madshare server is a feature layered on top of that, never a
precondition.

**Status:** the backend is **embedded** — madshare runs in this process and owns
the library (level 2a, first three sub-parts). Browse, search, playback and the
queue run off it; the provisional scanner and index this client shipped with are
gone. The HTTP client in `internal/madshare` is for level 1 (reaching somebody
else's server) and is not wired to a screen yet.

## What works

- **Folder scanning**, in place, by the server's own data-source machinery: one
  symlink per file, pointing at the original. Nothing is copied, moved or written
  into the scanned tree — the same hard invariant the server carries. A rescan
  skips files whose size and mtime are unchanged.
- **Browse**: artists → albums → tracks, with search across both artist roles.
  Every one of those decisions is made in SQL by the embedded backend; this client
  renders what arrived and re-derives none of it.
- **Tags, entities, cover art, duration, bitrate and fingerprints** all come from
  the backend's own ingest pipeline, running in-process — the same passes a server
  runs on an upload.
- **Playback**: MP3, FLAC, WAV and Ogg Vorbis, with a queue, shuffle, three-mode
  repeat, seeking and volume.
- **A folder that is not there** (unplugged drive, ejected card) says so, keeps its
  tracks listed, and marks them "not on this device right now". On a server a
  vanished import is an incident; on a player it is Tuesday.

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
- The dependency points client → server, always: `require daemonlord.ygg/madshare`
  plus `replace ... => ../` live **here**, never the other way round.
- **madshare's own `replace` directives do not reach us.** Only the main module's
  apply, so its local yggstack fork is repeated in this `go.mod` — without that
  line the build silently resolves upstream and loses three patches the mesh
  depends on. Keep it in step with the root `go.mod`.

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
internal/backend/    madshare, embedded: the data dir, the silent identity, folders
internal/library/    the browse view over it — fetch, shape for the screen, disc headers
internal/prefs/      the few settings that belong to this device (volume)
internal/queue/      play queue: index arithmetic, shuffle, repeat
internal/player/     decode, position, seek, queue advance — no audio device
internal/audio/      the audio device (cgo/ALSA). The ONLY package that needs one
internal/madshare/   HTTP client for a REMOTE madshare server (level 1, not yet wired)
android/             APK packaging (x86_64 build host only — see below)
```

Two layering rules are worth keeping:

- **`internal/player` does not import an audio device.** The output is injected as
  a `player.Sink`, so every decision — decode, seek, position, repeat, error
  recovery — is tested with a silent test double, and the one package that needs a
  sound card sits at the edge of the program. `internal/audio` is the whole of the
  cgo surface.
- **`internal/backend` is the only package that imports madshare.** Everything
  else goes through what it exposes, which is what keeps the embedded half from
  leaking into the widgets — and is also why the toolkit's `gioui.org/app` and
  madshare's `app` facade never meet in one file.

## Browse rows are fetched, not queried per frame

Every list is a database call into the embedded backend, and a Gio layout function
runs sixty times a second. So the UI **fetches on navigation and holds the rows**:
clicking an artist loads their albums in a goroutine and invalidates the window.
A load in flight, an empty library and a failed read are three different messages —
they must never look alike.

## The one rule that is still a port

`internal/queue` is a **port** of `webui/static/js/queue-ops.js`, specified by
`docs/ui/player-and-queue.md`, and `internal/queue/queue_test.go` mirrors
`tests/js/queue-ops.test.mjs` case for case. Two clients that disagree about what
shuffle does cannot share a queue, so if one of those needs different expectations
from its twin, the contract has changed and the doc moves in the same commit.

Disc grouping (`internal/library/disc.go`) stays here too, but for a different
reason: it is a **display** rule. The server orders an album so that same-disc
tracks arrive contiguous; turning that into "Disc N" separators is the client's
job, and headers are visual only — they must never shift a track index.

The entity rules this package used to carry (`EffectiveArtist`, album keys, the
Unknown/Other buckets) are **gone**: the embedded backend decides them now, which
is the whole point of embedding it. `docs/ui/madplayer.md` §"What the server
already computes" is the list of things not to re-derive.

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

### madshare's own build tags

Both of the server's tags work on this binary, and the sizes are worth knowing
before deciding (linux/arm64, unstripped, measured 2026-08-08):

| Build | Size |
|---|---|
| default | 32 MB |
| `-tags nowebui` | 31 MB |
| `-tags "nowebui nofederation"` | 23 MB |

- **`nowebui` is the right default** even though it only saves a megabyte: this
  client ships its own interface, so the embedded HTML/CSS/JS is a second one
  nothing can reach.
- **`nofederation` is not**, tempting as the 8 MB is. Leaving the mesh compiled in
  means reaching it at level 2b is wiring rather than a rebuild — and 2b is the
  whole reason for embedding rather than talking HTTP.

## Running

```bash
go run ./cmd/madplayer
go test ./internal/...
```

On first start it opens on **Folders**: type or paste a path and press *Add
folder*. There is no native folder picker in Gio, so the path is validated before
it is accepted — a silently-ignored typo looks exactly like an empty library.

State lives in `~/.config/madplayer/` (`app.DataDir()` per platform, which is what
makes it right on Android): `madshare.db`, `links/` (one symlink per imported
file), `files/` and `variants/` for what the backend owns, plus `config.json` for
this device's own preferences. Deleting the directory loses the library index and
the imported-folder list — never any music, since nothing in there is a copy.

There is **no account and no login**: the identity the backend needs is generated
at first run, never shown, and its password is thrown away. Nothing serves, so
there is nothing to log in to (`docs/architecture/embedding.md` §"Silent
provisioning").

## Android

**The APK cannot be built on an aarch64 Linux host.** `gogio` resolves the NDK
through an `archNDK()` that ends in `panic("unsupported GOARCH: arm64")`, the NDK
ships no linux-aarch64 host toolchain, and the 16 KB-page emulation wall from
`../docs/architecture/android-app.md` sits behind both. `android/build-apk.sh`
builds on an x86_64 host and refuses loudly elsewhere; `adb` runs natively on
aarch64, so install-and-look still happens on the development machine.
