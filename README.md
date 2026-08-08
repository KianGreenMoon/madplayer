# madplayer

The native client for madshare — a Go GUI for desktop and mobile. Design and
rationale live in [`../docs/ui/madplayer.md`](../docs/ui/madplayer.md); this file
covers only how to work on the code.

**Status:** toolkit decided (**Gio**, see `spike/README.md`), level 1 not started.
Nothing here is a product yet.

## Its own Go module, on purpose

`madplayer/` is a **separate module** (`daemonlord.ygg/madplayer`), not part of
the root `daemonlord.ygg/madshare` one. A GUI toolkit's `require` and `go.sum`
lines in the server's `go.mod` would be client code arriving in the server by the
back door — which is the one thing this branch exists to prevent.

Consequences:

- The repo root's `go build ./...` / `go test ./...` **do not** cover madplayer.
  Build and test it from this directory.
- Level 2 (embedding the backend in-process) adds
  `require daemonlord.ygg/madshare` plus a `replace ... => ../` **here**, never
  the other way round. The dependency points client → server, always.

## Branch discipline

This lives on the temporary `madplayer` branch and moves to its own repo later,
so the split stays a `git subtree split -P madplayer`:

- **No commit may touch `madplayer/` and anything outside it.** A commit is
  either client or server, never both. That is what lets a server-side fix reach
  the main branch on its own.
- Server changes are made on `aidev` and merged **forward** into `madplayer`.
  `madplayer` is never merged back.
- The target is **zero** server changes. The HTTP API is the shared contract and
  is meant to be complete; a genuine gap is a server design decision, not
  something the client works around.
- `../docs/ui/*` is the cross-client contract, so editing one of those docs is a
  server commit. Client-only notes belong in this file.

## Layout

```
internal/madshare/   HTTP client for a madshare server — durable
spike/               the toolkit-decision screen; promoted out of spike/ at level 1
  internal/demo/     fixture corpus + library walk that feed it
  gio/               track list + player bar
  android/           APK packaging (x86_64 build host only — see spike/README.md)
```

`internal/madshare` is deliberately a thin mirror of the server's JSON. Every
rule that could be re-derived client-side — which names are artists, which file
to play, sort order, disc grouping, availability — is decided server-side, and
this package carries the answer. The list is in
[`../docs/ui/madplayer.md`](../docs/ui/madplayer.md) §"What the server already
computes"; re-deriving any of it produces a client that quietly disagrees with
the web UI about what the library contains.

## Build prerequisites (Linux)

Gio is cgo. On Fedora both of these are optional — each backend has a fallback —
but without them you must pass the matching tag on every build:

```bash
sudo dnf install -y libxkbcommon-x11-devel vulkan-headers
```

- `libxkbcommon-x11-devel` — the X11 backend; without it build `-tags nox11`
  (Wayland only).
- `vulkan-headers` — the Vulkan backend; without it build `-tags novulkan`
  (falls back to OpenGL ES).

## Running

```bash
# fixtures: 5000 rows, multi-script text — the scroll and text-rendering test
go run ./spike/gio

# against a real server (start one from the repo root first)
MADPLAYER_BASE=http://localhost:3000 MADPLAYER_TOKEN=<api token> go run ./spike/gio
```

Gio is **native Wayland** on a Wayland session — it compiles both backends in and
picks at runtime, verified by running with `DISPLAY` unset.

Android: see `spike/README.md` §Android. Short version — **the APK cannot be built
on this aarch64 host**; `gogio` panics on `linux/arm64` by construction.
`spike/android/build-apks.sh` builds it on an x86_64 box, and `adb install` works
from here.

| Env | Meaning |
|---|---|
| `MADPLAYER_BASE` | madshare base URL. Unset → generated fixtures. |
| `MADPLAYER_TOKEN` | API token from `/settings`. Unset → anonymous, i.e. the guest listing. |
| `MADPLAYER_ROWS` | fixture row count (default 5000). |

The spikes show which of those two modes they are in, in the header. A spike that
silently fell back to fixtures would have you judging a toolkit on the wrong data.
