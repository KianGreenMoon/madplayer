# Toolkit decision: Gio

`docs/ui/madplayer.md` named the UI toolkit as the one blocking unknown, and set
the way to settle it:

> build one real screen (a track list + the player bar) in the candidate toolkit,
> compile it to the actual target, and **look at it** — load size, scroll feel,
> text rendering, "real app vs. game UI". The feel is the thing you cannot take on
> faith.

Both candidates were built to that brief: the same screen, over the same rows
(`internal/demo`), so nothing but the toolkit could differ. **Gio won on the look,
which is the criterion the doc set.** The Fyne build has been deleted; what
follows is what was measured before it went, so the decision stays auditable.

## Measured (Fedora, aarch64, Wayland, 5000 rows)

| | Gio v0.10.1 | Fyne v2.8.0 |
|---|---|---|
| Binary, unstripped, all backends | **14.2 MiB** | 28.4 MiB |
| Native Wayland (runs with `DISPLAY` unset) | yes | yes |
| Extra system dev packages | 2, both optional — each backend has a fallback | 1, **mandatory**: will not link without `libXxf86vm-devel` |
| Renders 5000 rows | yes | yes |
| Startup noise | none | a 3-line migration warning unless built `-tags migrated_fynedo` |

Build times were never recorded: the first numbers observed were polluted by an
earlier failed compile having warmed the deps, and a half-measured number is
worse than none.

Two things were deliberately kept out of the comparison, because neither could
discriminate between the candidates:

- **Audio decoding** — the same burden either way (oto/beep), so the transport is
  a fake clock. It remains real work, listed under §"The native client's own
  burdens" in the design doc.
- **Icons** — both used text buttons. Fyne ships a themed icon set; Gio needs
  `golang.org/x/exp/shiny/materialdesign/icons`. That asymmetry is now Gio's cost
  to pay, not a thing to hide.

## What survives

`gio/main.go` is a spike, not a foundation: one flat list, a fake transport, no
drill-down, no auth screen. It stays here until level 1 promotes it out of
`spike/`. What is already durable is `../internal/madshare` — the HTTP client,
written against the server's real handler JSON.

`internal/demo` stays too. Its multi-script fixture corpus — Latin with
diacritics, Cyrillic, Greek, CJK, Hangul, Arabic, Hebrew, Devanagari, an emoji,
and a title long enough to force elision — is the text-rendering regression test
for every list this client ever draws, and 5000 rows is a scroll load no real dev
library reaches.

## Android

**The APK cannot be built on this aarch64 host, and not for a reason that can be
worked around.** `gogio` resolves the NDK through an `archNDK()` that ends in

```go
panic("unsupported GOARCH: " + runtime.GOARCH)
```

on Linux/arm64 (`gioui.org/cmd/gogio/androidbuild.go`; `golang.org/x/mobile`'s
gomobile has the identical shape). Only darwin/arm64 is exempt — it borrows the
x86_64 toolchain under Rosetta. Behind that: the NDK ships no linux-aarch64 host
toolchain, and behind *that* is the 16 KB-page emulation wall already documented
in `docs/architecture/android-app.md`. Three walls, not one flag.

`android/build-apks.sh` builds on an x86_64 host and refuses loudly here. `adb`
runs natively on aarch64, so install-and-look still happens on this machine —
the same split the Capacitor app already uses.

On the phone the spike runs **standalone**: the fixture corpus is compiled in, so
there is no server to point at and no env var to set. Scroll feel and text
rendering on the actual target is the whole reason to look at a phone build.
