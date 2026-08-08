module daemonlord.ygg/madplayer

go 1.26.4

require (
	daemonlord.ygg/madshare v0.9.0
	gioui.org v0.10.1
	github.com/gopxl/beep/v2 v2.1.1
)

// The embedded backend, pinned to a released tag and upgraded on purpose — that
// is what madshare's tagging buys (docs/ui/madplayer.md §"Versioned dependency,
// not a vendored copy").
//
// The replace is what makes the require resolvable today, and it is a stand-in
// rather than the intended end state. madshare's module path is
// daemonlord.ygg/madshare, a Yggdrasil-only name that cannot serve go-import
// metadata over the public internet, and Go requires a replacement module's
// go.mod to declare the path it is required as — so pointing this at
// github.com/KianGreenMoon/madshare is rejected outright, not merely
// inconvenient.
//
// Upstream is https://github.com/KianGreenMoon/madshare. The day that module
// declares itself as github.com/KianGreenMoon/madshare, this whole block becomes
// one require line and the checkout beside us stops being load-bearing.
replace daemonlord.ygg/madshare => ../mediashare

// madshare's own replace does NOT reach us: only the MAIN module's replace
// directives apply, so its local yggstack fork has to be repeated here or the
// build silently resolves upstream and loses three patches the mesh depends on
// (third_party/yggstack/MADSHARE-PATCH.md). Keep in step with the root go.mod.
replace github.com/yggdrasil-network/yggstack => ../mediashare/third_party/yggstack

require (
	gioui.org/shader v1.0.8 // indirect
	github.com/Arceliar/ironwood v0.0.0-20260613025018-d50055b11f5e // indirect
	github.com/Arceliar/phony v0.0.0-20220903101357-530938a4b13d // indirect
	github.com/BurntSushi/toml v1.6.0 // indirect
	github.com/bits-and-blooms/bitset v1.24.5 // indirect
	github.com/bits-and-blooms/bloom/v3 v3.7.1 // indirect
	github.com/coder/websocket v1.8.15 // indirect
	github.com/dhowden/tag v0.0.0-20240417053706-3d75831295e8 // indirect
	github.com/disintegration/imaging v1.6.2 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/ebitengine/oto/v3 v3.4.0 // indirect
	github.com/ebitengine/purego v0.9.0 // indirect
	github.com/go-chi/chi/v5 v5.2.5 // indirect
	github.com/go-text/typesetting v0.3.4 // indirect
	github.com/gologme/log v1.3.0 // indirect
	github.com/google/btree v1.1.3 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/hajimehoshi/go-mp3 v0.3.4 // indirect
	github.com/hjson/hjson-go/v4 v4.6.0 // indirect
	github.com/icza/bitio v1.1.0 // indirect
	github.com/jfreymuth/oggvorbis v1.0.5 // indirect
	github.com/jfreymuth/vorbis v1.0.2 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/mewkiz/flac v1.0.12 // indirect
	github.com/mewkiz/pkg v0.0.0-20230226050401-4010bf0fec14 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/quic-go/quic-go v0.60.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/wlynxg/anet v0.0.5 // indirect
	github.com/yggdrasil-network/yggdrasil-go v0.5.14 // indirect
	github.com/yggdrasil-network/yggstack v0.0.0-20260619214331-c39db65e5bcc // indirect
	golang.org/x/crypto v0.53.0 // indirect
	golang.org/x/exp/shiny v0.0.0-20250408133849-7e4ce0ab07d0 // indirect
	golang.org/x/image v0.26.0 // indirect
	golang.org/x/net v0.56.0 // indirect
	golang.org/x/sys v0.46.0 // indirect
	golang.org/x/text v0.38.0 // indirect
	golang.org/x/time v0.7.0 // indirect
	gvisor.dev/gvisor v0.0.0-20250812171554-968e93457fe6 // indirect
	modernc.org/libc v1.72.3 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.11.0 // indirect
	modernc.org/sqlite v1.50.1 // indirect
)
