# madplayer

A native music player in Go, for desktop and mobile. Design and rationale live in
madshare's `docs/ui/madplayer.md` — that doc is a **cross-client contract** and
stays there on purpose; this file covers how to work on the code.

**It is an offline player first.** Point it at your music folders and it scans,
indexes and plays them — no server, no account, no network, nothing to sign in
to. Reaching a madshare server is a feature layered on top of that, never a
precondition.

**Status:** a usable desktop player. The backend is **embedded** (level 2a), the
client **signs in to remote madshares** (level 1), and this device's library and
every server's are browsed as one merged list. It has cover art, keyboard
control, an editable queue that survives a restart, and it answers the media
keys and fills the desktop's media widget.

Level 2b — the mesh — is **built**: the device can become a madnetwork node,
keeps its standing with each home server, **plays network tracks off the swarm
with the relay as the fallback**, and **keeps them on this device** as ordinary
files in a folder it manages. The three questions `docs/ui/madplayer.md`
§"Where the bytes live" left open were settled by the owner on 2026-08-15 and
are written into that doc.

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
  repeat, seeking and volume — from the buttons or from the keyboard (Space,
  the arrows, `N`/`P`, `S`, `R`, `/`; the full list is printed in Settings).
- **Media keys and the desktop's media widget**, over MPRIS. See *On the
  desktop's media bus* below.
- **Cover art**, read out of the music itself. See *Where a cover comes from*
  below.
- **Keeping network music**: a track from a server or the madnetwork can be
  saved onto this device as an ordinary file. See *Keeping network music* below.
- **Queue editing**: *Play next* and *Add to queue* on every track row (on
  hover) and on an album header, and ↑/↓/× in the queue panel. Clicking a row
  still replaces the queue with the view you clicked in — that is the contract
  in `docs/ui/player-and-queue.md`, and these are the ways of choosing music
  that do not throw away what you had built.
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

## Keeping network music

`docs/ui/madplayer.md` §"Where the bytes live" settled this on 2026-08-15, and
the shape is one idea rather than three answers: **the destination is a folder
madplayer manages** — `<music dir>/madplayer` by default, overridable in
Settings — laid out `Artist/Album/NN - Title.ext`. That answers "which of the
scanned folders?" by not asking it, while keeping the music inside the music
directory instead of hiding it in application storage.

- **It lands as an ordinary library row.** The folder is a data source like any
  other, so a kept track is links-backed exactly like scanned music — one kind
  of entry, which is what the design requires.
- **Already there is decided by content hash, not by path**, so the same audio
  asked for twice is recognised even if its tags changed in between. Materialize
  gets pressed twice; this is what makes that free.
- **A taken NAME and a failed WRITE are different things.** Anything occupying
  the name — a file, a directory, a dangling symlink — is a collision and takes
  a short content-hash suffix, once. A write that fails is refused and reported:
  no retry under a second name, because working around a broken disk is how a
  music folder gains files nobody meant.
- **The folder is madplayer's.** A file somebody else puts in it is ignored with
  a warning in Settings — not adopted, and not moved on their behalf. Telling
  ours from theirs needs a record (`materialized.json`), which lives in the data
  directory rather than the music folder so it survives the library database
  being thrown away — which is the exact case the re-register pass exists for.
- **Nothing is created until something is kept.** An earlier version probed the
  folder at startup, and probing means creating, so every launch and every test
  run left an empty `madplayer` folder in somebody's music directory. One did,
  in `~/Musik`, which is how it was found.
- **The fallback is never silent.** A folder you CHOSE that cannot be written is
  refused; the DEFAULT falls back to the app's data directory with a sentence
  saying so — on Android that is private storage no file manager can browse,
  which breaks the promise this design makes.
- **"Save with technical names"** switches to hash-paths wholesale, for a
  filesystem that will not take the human ones. A preference and not an
  automatic fallback: a program that silently changed its own layout halfway
  through a collection would be worse than one that asked.

The button says **"Keep on this device"**, and that is the cross-client rule
rather than this client's local preference: `docs/ui/madnetwork-page.md` decides
the word by **where the content lands** — into a server's library it is
*Materialize*, onto a person's own device it is this. Two acts, two words. A
server is not "this device" and a player has no catalogue to admit anything to,
so one word would have been wrong on one of them whichever won.

## The queue survives a restart

`docs/ui/player-and-queue.md` §"Persistence & resume" has had this rule since the
web UI shipped, and the pieces on this side were **built and never wired**:
`player.Snapshot` and `player.Restore` existed with nothing calling them, so
closing the window threw the queue away. `internal/ui/queuestate.go` connects
them.

- Saved: the visible order, the current index, the **original un-shuffled
  order**, the shuffle state, the repeat mode and the position within the
  current track. Written on every queue change, every 5 s while playing (the
  position moves continuously and there is nothing to hook), and at exit.
- **Restored paused**, pointing at the track. Pressing play resumes mid-track;
  clicking a row starts that row from the beginning. Both fall out of one rule:
  the position belongs to a named row (`player.ResumeAt`), is consumed the first
  time that row loads, and is dropped by every explicit navigation. A bare
  offset with no owner would seek whatever track happened to load next.
- It lives in **`queue.json`, not `config.json`**, deliberately: the settings
  file holds API tokens and is 0600, folding the queue in would rewrite the
  credential file every five seconds while music plays, and a queue that cannot
  be parsed must cost you the queue rather than your sign-ins.
- Clearing the queue **removes** the file. Leaving the last state on disk to be
  found at the next launch is not what "clear" means.

`ui.newApp` takes the settings directory so a test can point at a temporary one
*before* anything reads or writes it. Replacing the store afterwards is not the
same thing — by then the saved queue has been read and the background writer is
already aimed at the real directory, which is how a test run comes to overwrite
the queue of whoever was listening to music at the time.

## On the desktop's media bus

`internal/mpris` exports `org.mpris.MediaPlayer2`, which is how a Linux desktop
knows what is playing: it is what makes the XF86Audio keys on a keyboard reach
*this* program, what fills the media widget in GNOME's calendar drop-down and
KDE's system tray, and what `playerctl` speaks. Verified live with
`playerctl -p madplayer …` — status, metadata (cover included), position, seek,
shuffle, loop, volume and Quit all work.

- **Quit needs a repaint to take effect** (`ui.closeWindow`). Gio's X11 close
  sends the window a `WM_DELETE_WINDOW` ClientMessage with `XSendEvent` and never
  flushes Xlib's output buffer, so on an IDLE window the message sits there and
  the program refuses to quit. It appeared to work when first built purely
  because music was playing and the 200 ms tick was drawing frames. The
  `Invalidate` after `Perform` is what posts it.
- **It is optional and never fatal.** A machine with no session bus is a normal
  machine; a failure is one log line and the program carries on with its window.
  A nil `*mpris.Service` is usable and does nothing, which is the whole of the
  caller's error handling.
- **It is a view, not a second player.** Nothing there holds playback state —
  every property is computed from the player when asked, so the bus and the
  window cannot disagree.
- **`Position` is written, not signalled.** The D-Bus properties helper stores
  values rather than computing them, so a continuously-changing property has to
  be pushed; it is declared `EmitFalse` and updated on the UI's own 200 ms tick,
  which is exactly the spec's model (clients poll `Position`, and `Seeked` covers
  the discontinuities).
- **The writable properties dispatch their work to a goroutine, and that is a
  deadlock fix rather than tidiness.** The properties helper holds its lock
  across the callback; changing the player from inside one reaches the player's
  change hook → `Update` → a property write → the same non-reentrant lock, and
  `playerctl shuffle On` hangs until it times out.
- `player.Play`/`Pause`/`SetShuffle`/`SetRepeat` exist because a remote control
  sets states rather than flipping them. Answering "Play" with a toggle pauses a
  player that was already playing, which is the classic media-key bug.
- `player.Paused` exists because *Paused* and *Stopped* are two different states
  on the bus and identical on screen.
- **`mpris:artUrl` is a URL, so it needs a FILE.** The widget fetches it in
  another process, where bytes in this one's memory are of no use. A folder
  cover already is a file and is handed over as it is; embedded art is written
  out once into `covers/` under the data dir (`artwork.Cache.SpillDir`). The
  path is escaped through `net/url` rather than concatenated — music
  directories are full of spaces and `#`.

## Where a cover comes from

`internal/artwork` reads the **file**, not the library index, and that is a
deliberate divergence from the rule that the backend decides everything.
madshare's ingest does extract cover images — it built variants for this
library on first scan — but the embedder facade reports only whether an album
*has* one (`database.AlbumEntry.HasImage`) and offers no way to reach the bytes:
there is no image twin of `Library.BlobPath`. Reading the audio file's own tags
is therefore not a re-derivation of a server rule, it is the only route this
client has. **A facade call would be the better long-term answer** and is worth
adding to madshare when somebody is in there anyway.

Two properties are worth keeping even after that call exists: it works with the
network unplugged, and it cannot disagree with the file.

- **Embedded art wins, the folder is the fallback.** A compilation has one
  folder and twelve different covers, so the picture frame in the track's own
  tags is the artist's answer for *that* track. Failing that, the best-named
  image beside the music is used (`cover`, `folder`, `front`, …, then any single
  image — a sleeve scanned as `IMG_2231.jpg` is still that album's cover).
- **The folder searched is the ORIGINAL's, not the link's.** A scanned track is
  reached through `links/<hash>/`, a directory holding one symlink and never a
  cover, so the path is resolved before its folder is read. This is load-bearing:
  without it, no folder cover is ever found for a scanned library.
- **Album rows ask only the device library** (`library.DeviceAlbumTracks`). A
  cover needs a file path, only a track carries one, and asking every signed-in
  server for the tracks of every album on screen would turn scrolling into a
  fan-out. A server's album shows the placeholder until something from it plays
  — at which point the download is on disk and the player bar reads the cover
  out of it, which is what `player.CurrentPath` exists for.
- Covers are scaled to 320 px and the cache is bounded. An untouched 3000×3000
  booklet scan is 36 MB of pixels for a 40 dp thumbnail.
- **A cover can also be asked for as a file** (`Cache.File`), which is what the
  media bus needs; see *On the desktop's media bus*.

## Remote tracks are streamed, and cached as they arrive

**This section used to say the opposite, and it was wrong.** The claim was that
go-mp3 walks every frame header before reporting a length and beep's flac takes
its seek path only over an `io.ReadSeeker`, so the whole file had to be on disk
first. Both halves are true **only for a seekable reader** — go-mp3 skips the
walk entirely when its source is not an `io.Seeker` (`decode.go`: `if _, ok :=
d.source.reader.(io.Seeker); !ok { return nil }`), and beep's flac picks
`flac.New` over `flac.NewSeek` on the same test. What forced the wait was
handing the decoder an `*os.File`, not the decoder.

Measured on real music before this was rebuilt:

| | old behaviour | decoded from a non-seekable reader |
|---|---|---|
| 6.6 MB MP3 | wait for all 6.6 MB | decoder ready in **60 ms**, having read **0.0%** |
| 37 MB FLAC | wait for all 37 MB | ready in **107 ms** at **0.2%**; first audio at **156 ms** |

So `blobcache.Stream` hands back a reader over the download as it lands: it
blocks at the tail rather than reporting an end that has not happened, and it is
**deliberately not an `io.Seeker`**, because that is the flag the decoders read.
A blob already cached comes back as an ordinary `*os.File`, so replaying a track
is unchanged.

Two costs, both paid on purpose:

- **A streaming track cannot be scrubbed.** `player.Seekable` reports it and the
  seek bar goes quiet. This is not merely cosmetic: beep's mp3 decoder
  *panics* on `Seek` over a non-seekable source, so the guard in `player.Seek`
  is what stands between a drag and the process dying inside the audio path.
- **A streaming MP3 does not know its own length**, since that is what the walk
  computed. The player bar falls back to the queue item's duration, which the
  library knew all along. FLAC needs none of this — `STREAMINFO` carries the
  sample count, so it reports its length even while streaming.

The cache itself is unchanged in every other way:

- The **cache is keyed by content hash**, so the same audio offered by two
  servers is one file, and a server changing address orphans nothing.
- The **directory is authoritative and there is no index** — the rule the
  server's madnetwork cache settled on. Last use is the file's mtime, touched on
  every hit (atime is unreliable on a relatime mount, which is nearly all of
  them).
- **The ceiling is madshare's setting, not this client's**, and it has the same
  three layers here as on a server: a default, an override, and an empty box
  meaning "use the default". `backend.CacheCeiling` reports all three;
  the **Settings** panel edits the override. A second copy in `config.json` would
  be a number that can disagree with itself.
- **This client's default is 2 GiB, supplied as config** —
  `playerConfig` sets `Federation.CacheMaxMB`, the layer a server fills from its
  TOML file. That is what makes clearing an override land back on 2 GiB rather
  than on "no limit". A server ships 0 instead, because a guessed ceiling would
  start deleting content on a node that already has some; a player's cache starts
  empty, so it has no such history to protect.
- One policy number, one enforcer per cache: madshare sweeps the swarm's cache,
  this sweeps `remote/`.
- **Playback is asynchronous** because of this: a download cannot happen on the
  goroutine that handled the click. The next queue item is prefetched, so only
  the first remote track in a run pays the gap.

### Two ways to get the bytes

`internal/remote` owns the choice, and it is the only place that knows there is
one. The **swarm** asks whoever holds the blob; the **relay** asks the one server
that named it. The relay is not a degraded mode — it is level 1, it works with no
mesh at all, and every reason the swarm has to decline lands there silently: no
node on this device, a track named without a content hash, no vouch from that
server yet, or nobody holding it. Only a swarm fetch that *tried and failed* logs
a line, because a mesh that quietly never works looks exactly like one that does.

- **The mesh is ON by default** (owner, 2026-08-15, reversing the original
  decision) and **takes effect at the next restart** — whether this device is a
  node is decided in the config the backend is built from. Switch it off in
  Settings › *Use the madnetwork*; with it off, every fetch is the relay and the
  swarm never runs.

  Turning it off **sticks**, and that took care: `prefs.Mesh` has no
  `omitempty`, because with one a `false` writes nothing, the next launch reads
  the absent key as the default, and a player somebody deliberately took off the
  network quietly rejoins it. Absent means "never chosen" and takes the default;
  `false` is a decision and is written down. (The volume setting had exactly this
  bug and it is fixed the same way.)

  A machine with no **fpcalc** degrades rather than fails: the backend refuses to
  federate without it, `MeshProblem` carries the sentence, and the Settings line
  says so.
- **The enrolment used to never learn its servers** (fixed 2026-08-15).
  `applyServers` is the one place that tells every consumer which servers there
  are, and it skipped a nil enrolment silently — while running *before* the mesh
  was built. So the enrolment loop started with an empty server list, nothing
  ever filled it, no capability token was ever fetched, `Present` returned false
  and every swarm fetch declined without a word. The madnetwork had never once
  been used. It is now structural rather than positional: the list is computed
  and kept whether or not there is an enrolment to hand it to, and handed over
  the moment one exists, so the order cannot matter again.
- **The swarm STREAMS, and the budget bounds the first byte** (2026-08-15).
  It used to bound the whole transfer, and that was right while playback waited
  for the last byte: a slow swarm meant silence, so the relay had to be handed a
  live context while some of the allowance was left. Now a track plays from the
  bytes as they land, so a long transfer is not a long wait. Measured against
  this device's own home server: the swarm delivered 19.3 MiB in 18.9 s and lost
  a 20 s whole-transfer budget **by one second, every time**, after spending the
  twenty. With the deadline on the start instead: **audio at 2.1 s, transfer
  complete at 18.3 s**, no relay fallback.

  `backend.StreamBlob` reads the transfer's own partial file through
  `Transfer.WaitFor` / `Available` — the same pair madshare's
  `/api/madnetwork/stream/{hash}` relay uses, so these are per-chunk-verified
  bytes it already considers safe to serve. No madshare change was needed.

  **The cost, paid deliberately:** once swarm bytes reach the cache there is no
  falling back, so a swarm that dies *mid-track* fails that play instead of
  being rescued by the relay. Appending a second source's bytes to the first's
  decodes as noise, which is worse than a track that failed. A swarm that never
  *starts* is still an ordinary decline, and the relay still takes it.
- **`FetchBlob` still blocks** and is now used only for keeping a track on this
  device, where a half-copied file would be worse than a wait.
- **The swarm gets a budget, not the caller's whole deadline**
  (`remote.DefaultSwarmBudget`, 20 s). This is not a tidiness rule, it is the
  difference between music and none: measured against a real server, the relay
  delivered a 20 MB track in 3.8 s and the swarm took 4 m 05 s, so an unbounded
  mesh attempt spent the entire allowance and handed the relay a context that was
  already dead. Expiring is an ordinary decline — nothing has been written, so
  the relay takes over with what is left.
- **Holders come from `GET /api/madnetwork/holders/{hash}`, always.** The design
  doc offers a browse row's own `versions[].holders[]` as the cheaper source and
  this client never has one: those rows are the `/madnetwork` page's, and what
  madplayer merges is each server's ordinary library, whose track rows carry no
  holders.
- **Mesh fetches run one at a time.** The mesh carries one vouch and it is
  installed process-wide, so presenting the token and fetching is one indivisible
  step — otherwise a prefetch for another server's track swaps the token out from
  under a fetch already running.
- **Once bytes have landed, there is no falling back.** The swarm's copy is
  written to the cache only after the transfer is complete and verified, and if
  that write fails part-way the relay is not tried: a second source's bytes
  appended to the first's decode as noise instead of failing.
- **A swarm fetch stores the blob twice, on purpose.** `backend.FetchBlob` lands
  it in madshare's own `cache/madnetwork/` — hash-named, no extension — and that
  is the only directory this node seeds from and the only one `Holdings`
  advertises. `internal/remote` then copies it into `remote/` under a name the
  decoders can read, since they pick by extension. The corollary is worth
  knowing: **a device that only ever used the relay seeds nothing**, because
  relay downloads never touch the seeding cache.

**The credential is an API token, never the password.** `SignIn` spends the
password once — log in, mint a token, drop the session — and what is kept is a
credential that server lists by name and can revoke. `config.json` therefore
holds tokens and is written `0600`; there is no keyring here, and that is a real
limitation rather than an oversight. Signing out forgets the token on this
device and says so: revoking it happens on the server.

## The madshare dependency

madplayer requires madshare as an ordinary Go module, pinned to a released tag
and upgraded on purpose. That is what the server's release tagging buys: a
client pins a known-good server, and a server change lands here when somebody
chooses it rather than the moment it is written.

Today that require is resolved by a **`replace` to a checkout beside this one**,
so the two directories have to sit together:

```
madshare/       madshare  (module daemonlord.ygg/madshare)
madplayer/      this repo
```

The replace is a stand-in, not the intended end state, and the reason is worth
knowing before anyone tries to remove it. Upstream is
<https://github.com/KianGreenMoon/madshare>, but madshare declares its module
path as `daemonlord.ygg/madshare` — a Yggdrasil-only name that cannot serve
go-import metadata over the public internet — and Go requires a replacement
module's `go.mod` to declare the path it is required as. Pointing the replace at
the GitHub URL is therefore rejected outright rather than merely awkward. The day
madshare renames its module to `github.com/KianGreenMoon/madshare`, this collapses
to a single `require` line and the checkout beside us stops being load-bearing.

**`third_party/yggstack` is replaced too**, at
`../madshare/third_party/yggstack`, and that one is load-bearing right now: only
the main module's replaces apply, so without it the build silently resolves
upstream yggstack and loses three patches the mesh depends on. Keep it in step
with madshare's root `go.mod`.

This is a separate module from madshare on purpose, and always was: a GUI
toolkit's `require` and `go.sum` lines in the server's `go.mod` would be client
code arriving in the server by the back door. The dependency points client →
server, always, never the other way round.

**`docs/ui/*` lives in madshare**, because it is the contract both clients follow.
A change to one of those docs is a madshare commit; client-only notes belong in
this file.

## Layout

```
cmd/madplayer/       the program
packaging/           the desktop entry, the icon generator, the user-level installer
internal/backend/    madshare, embedded: the data dir, the silent identity, folders
internal/artwork/    cover art, read out of the music file or the folder beside it
internal/materialize/ where network music is kept, what it is named, and what is ours
internal/mpris/      the desktop's media bus: media keys, the system media widget
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

## Installing it as a desktop app

```bash
sh packaging/install-desktop.sh
```

Binary, icon and menu entry, all under `$HOME` — no root, and nothing the
package manager owns is touched. Removing the three paths it prints is the
uninstall.

The `.desktop` file is not only about the menu. MPRIS clients resolve the
`DesktopEntry` property this program publishes against the freedesktop entries,
and that is where the desktop's media widget gets the **name and icon** it draws
beside the transport — so without it the widget can drive the player and has
nothing to call it. The file therefore has to be `madplayer.desktop`, matching
the bus name `org.mpris.MediaPlayer2.madplayer`.

The icon is generated rather than committed (`packaging/icon`), and it is the
same generator the APK build uses, so the two cannot drift.

## Running

```bash
go run ./cmd/madplayer
go test ./internal/...
```

On first start it opens on **Settings**: type or paste a folder path and press
*Add folder*. There is no native folder picker in Gio, so the path is validated
before it is accepted — a silently-ignored typo looks exactly like an empty
library. The same panel holds the download limit, because both answer the
question of what this device keeps on disk. **Servers** is the other way to get
music: an address, a username and a password. A person who plays only from a
server is never sent back to add a folder.

State lives in `~/.config/madplayer/` (`app.DataDir()` per platform, which is what
makes it right on Android): `madshare.db`, `links/` (one symlink per imported
file), `files/` and `variants/` for what the backend owns, `remote/` for
downloaded audio, `config.json` for this device's own preferences and its
server credentials, `queue.json` for what was playing, and `covers/` for cover
art copied out of audio files so the desktop's media widget can fetch it. Deleting the directory loses the library index, the
imported-folder list and the sign-ins — never any music, since nothing in there
is a copy.

Note which of those holds what: `config.json` has the volume and the server
credentials, while anything that is a **policy** — the download ceiling — is in
`madshare.db`, because it is madshare's setting and this client is only one of
its two surfaces.

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
madshare's `docs/architecture/android-app.md` sits behind both. `android/build-apk.sh`
builds on an x86_64 host and refuses loudly elsewhere; `adb` runs natively on
aarch64, so install-and-look still happens on the development machine.
