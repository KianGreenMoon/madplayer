# madplayer

A native music player in Go, for desktop and mobile. Design and rationale live
in [`../docs/ui/madplayer.md`](../docs/ui/madplayer.md); this file covers how to
work on the code.

**It is an offline player first.** Point it at your music folders and it scans,
indexes and plays them — no server, no account, no network, nothing to sign in
to. Reaching a madshare server is a feature layered on top of that, never a
precondition.

**Status:** the backend is **embedded** (level 2a) and the client **signs in to
remote madshares** (level 1). This device's library and every server's are
browsed as one merged list. Still to come: the mesh (level 2b) and the
human-readable materialize target.

## What works

- **Folder scanning**, in place, by the server's own data-source machinery: one
  symlink per file, pointing at the original. Nothing is copied, moved or written
  into the scanned tree — the same hard invariant the server carries. A rescan
  skips files whose size and mtime are unchanged.
- **Browse**: artists → albums → tracks, with search across both artist roles.
  Every one of those decisions is made in SQL by the embedded backend; this client
  renders what arrived and re-derives none of it.
- **Remote servers**: sign in with a username and password, and that server's
  library appears in the same lists as your own. See *One list, several
  libraries* below.
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

## One list, several libraries

Signing in to a server does not open a second browser. Its artists, albums and
tracks are merged into the lists already on screen, and the merge rule is
**imported from the server, not invented here**: it is the one `/madnetwork`
already uses to fold catalogs from many nodes, because that is the same problem
— rows from different libraries that share no id space. Same text, one row; the
Unknown-artist and Other buckets last; a track keyed by disc, number and title
(`internal/library/merge.go`, which cites the SQL it mirrors).

Four consequences are worth knowing before reading that code:

- **A merged count is a lower bound**, rendered `23+`. Summing would
  double-count everything held in two places, which is what a merged view is
  full of. The maximum is the one number that is always true, since merging can
  only fold rows and never invent them.
- **Every copy is kept, and the local one plays.** Preferring local is not an
  optimisation, it is the offline case working: a track this machine holds plays
  with the network unplugged, whichever server also has it. The reverse also
  falls out — a track whose drive is unplugged still plays from a server.
- **Ids are per-library.** 41 on one server is not 41 on another, so nothing is
  addressed by an id without its source beside it (`library.Origin`). Drilling
  asks each library the row came from, with the id it has there.
- **One unreachable server is a footnote, not an error.** It is named above the
  rows; the rest of the music still lists. Only when *every* library fails is
  there a real error, because an empty list would say "you own nothing".

Re-ordering the merged list is the one place this client is allowed to sort — N
lists that each arrived ordered do not concatenate into an ordered list — and it
sorts by the server's own keys.

## Remote tracks are downloaded, not streamed

This is forced by the decoders, not chosen: go-mp3 walks every frame header
before it will report a length (`ensureFrameStartsAndLength`), and beep's flac
takes its seek path only over an `io.ReadSeeker`. Both mean the whole file has
to be on disk, so `internal/blobcache` fetches it and the decoder opens that.
There is no useful sense in which this client streams.

- The **cache is keyed by content hash**, so the same audio offered by two
  servers is one file, and a server changing address orphans nothing.
- The **directory is authoritative and there is no index** — the rule the
  server's madnetwork cache settled on. Last use is the file's mtime, touched on
  every hit (atime is unreliable on a relatime mount, which is nearly all of
  them).
- The ceiling defaults to **2 GiB** and is editable in the Servers panel. The
  number comes from the shape of the content — a FLAC album is roughly 300 MB —
  and has never met a real library, which is why being editable is the part that
  matters.
- **Playback is asynchronous** because of this: a download cannot happen on the
  goroutine that handled the click. The next queue item is prefetched, so only
  the first remote track in a run pays the gap.

**The credential is an API token, never the password.** `SignIn` spends the
password once — log in, mint a token, drop the session — and what is kept is a
credential that server lists by name and can revoke. `config.json` therefore
holds tokens and is written `0600`; there is no keyring here, and that is a real
limitation rather than an oversight. Signing out forgets the token on this
device and says so: revoking it happens on the server.

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
internal/library/    the merged browse view — sources, the merge rule, disc headers
internal/prefs/      the settings that belong to this device (volume, servers, cache)
internal/queue/      play queue: index arithmetic, shuffle, repeat
internal/player/     decode, position, seek, queue advance — no audio device
internal/audio/      the audio device (cgo/ALSA). The ONLY package that needs one
internal/madshare/   HTTP client for a REMOTE madshare server: browse and sign-in
internal/blobcache/  downloaded audio on disk, under a ceiling, LRU
internal/remote/     the join: queue item → the right server's client → the cache
android/             APK packaging (x86_64 build host only — see below)
```

Three layering rules are worth keeping:

- **`internal/player` does not import an audio device.** The output is injected as
  a `player.Sink`, so every decision — decode, seek, position, repeat, error
  recovery — is tested with a silent test double, and the one package that needs a
  sound card sits at the edge of the program. `internal/audio` is the whole of the
  cgo surface.
- **`internal/backend` is the only package that imports madshare's `app`.**
  Everything else goes through what it exposes, which is what keeps the embedded
  half from leaking into the widgets — and is also why the toolkit's
  `gioui.org/app` and madshare's `app` facade never meet in one file.
- **`internal/player` does not know what a server is.** A remote track reaches
  it as a `player.Fetcher` returning a path, so every playback decision is
  tested with a fake that touches no network, and the HTTP half stays in
  `internal/remote`.

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
**Servers** is the other way to get music: an address, a username and a password.
A person who plays only from a server is never sent back to add a folder.

State lives in `~/.config/madplayer/` (`app.DataDir()` per platform, which is what
makes it right on Android): `madshare.db`, `links/` (one symlink per imported
file), `files/` and `variants/` for what the backend owns, `remote/` for
downloaded audio, plus `config.json` for this device's own preferences and its
server credentials. Deleting the directory loses the library index, the
imported-folder list and the sign-ins — never any music, since nothing in there
is a copy.

There is **no local account and no local login**: the identity the backend needs
is generated at first run, never shown, and its password is thrown away. Nothing
serves, so there is nothing to log in to
(`docs/architecture/embedding.md` §"Silent provisioning"). The sign-in in the
Servers panel is a different thing entirely — it authenticates to somebody
*else's* madshare, and the account and its password live there.

## Android

**The APK cannot be built on an aarch64 Linux host.** `gogio` resolves the NDK
through an `archNDK()` that ends in `panic("unsupported GOARCH: arm64")`, the NDK
ships no linux-aarch64 host toolchain, and the 16 KB-page emulation wall from
`../docs/architecture/android-app.md` sits behind both. `android/build-apk.sh`
builds on an x86_64 host and refuses loudly elsewhere; `adb` runs natively on
aarch64, so install-and-look still happens on the development machine.
