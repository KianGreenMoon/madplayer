package about

import (
	"runtime/debug"
	"strings"
	"testing"
	"time"
)

// The three shapes a build comes in, because the licence says something
// different about each: a build that can be matched to a published commit, one
// that cannot because the tree was dirty, and one with no VCS stamp at all
// (built outside a repository, or with -buildvcs=false).

func info(settings ...debug.BuildSetting) *debug.BuildInfo {
	return &debug.BuildInfo{
		GoVersion: "go1.26.4",
		Main:      debug.Module{Path: "daemonlord.ygg/madplayer", Version: "v0.0.0-20260815203658-e243d64cd80e"},
		Deps: []*debug.Module{
			{Path: madsharePath, Version: "v0.8.8"},
		},
		Settings: settings,
	}
}

func TestACleanBuildNamesItsCommitAndDate(t *testing.T) {
	b := fromInfo(info(
		debug.BuildSetting{Key: "vcs.revision", Value: "e243d64cd80e394af5f05919c8ab6d32f9bf79e9"},
		debug.BuildSetting{Key: "vcs.time", Value: "2026-08-15T20:36:58Z"},
		debug.BuildSetting{Key: "vcs.modified", Value: "false"},
	))

	if !b.Identified() {
		t.Fatal("a clean build from a repository could not be identified")
	}
	if b.Short() != "e243d64" {
		t.Errorf("Short() = %q", b.Short())
	}
	if got := b.BuildLine(); !strings.Contains(got, "e243d64") || !strings.Contains(got, "August 2026") {
		t.Errorf("BuildLine() = %q, want the commit and its date", got)
	}
	if !b.Time.Equal(time.Date(2026, 8, 15, 20, 36, 58, 0, time.UTC)) {
		t.Errorf("Time = %v", b.Time)
	}
}

// A dirty tree is the case worth being loud about: no published source
// corresponds to this binary, so the panel must not say it does.
func TestADirtyBuildSaysSo(t *testing.T) {
	b := fromInfo(info(
		debug.BuildSetting{Key: "vcs.revision", Value: "e243d64cd80e394af5f05919c8ab6d32f9bf79e9"},
		debug.BuildSetting{Key: "vcs.modified", Value: "true"},
	))

	if b.Identified() {
		t.Fatal("a build with uncommitted changes claimed to match a commit")
	}
	if got := b.BuildLine(); !strings.Contains(got, "uncommitted") {
		t.Errorf("BuildLine() = %q, want it to admit the changes", got)
	}
}

// No stamp at all — a tarball build, or -buildvcs=false. It must not print an
// empty commit and look like a bug.
func TestABuildWithNoRepositoryBehindIt(t *testing.T) {
	b := fromInfo(info())

	if b.Identified() {
		t.Fatal("a build with no revision claimed to be identifiable")
	}
	if got := b.BuildLine(); got != "built from an unidentified tree" {
		t.Errorf("BuildLine() = %q", got)
	}
	if b.Short() != "" {
		t.Errorf("Short() = %q, want empty", b.Short())
	}
}

// The engine's version is half of what the Corresponding Source has to include,
// and a `replace` to a local checkout has no version of its own — printing
// "(devel)" would be a word that means nothing on anybody else's machine.
func TestTheEmbeddedEngineIsNamed(t *testing.T) {
	plain := fromInfo(info())
	if plain.Madshare != "v0.8.8" {
		t.Errorf("Madshare = %q, want the required version", plain.Madshare)
	}

	local := info()
	local.Deps[0].Replace = &debug.Module{Path: "../madshare", Version: "(devel)"}
	if got := fromInfo(local).Madshare; !strings.Contains(got, "v0.8.8") || !strings.Contains(got, "local") {
		t.Errorf("Madshare = %q, want the version and the fact that it came from a checkout", got)
	}
}

// The offer has to match the state of the world. Pointing somebody at a
// repository that is not up yet, and calling that compliance, is the failure
// this pins.
func TestTheSourceOfferMatchesWhatIsActuallyOnOffer(t *testing.T) {
	clean := fromInfo(info(
		debug.BuildSetting{Key: "vcs.revision", Value: "e243d64cd80e394af5f05919c8ab6d32f9bf79e9"},
		debug.BuildSetting{Key: "vcs.modified", Value: "false"},
	))
	offer := clean.SourceOffer()
	if Published {
		if !strings.Contains(offer, clean.Short()) {
			t.Errorf("offer = %q, want it to name the commit it corresponds to", offer)
		}
		return
	}
	if !strings.Contains(offer, "not public yet") || !strings.Contains(offer, Author) {
		t.Errorf("offer = %q, want it to admit the repository is not up and say who to ask", offer)
	}
}

// The notice is what somebody copies into a bug report or an audit, so it has to
// carry all four facts on its own.
func TestTheNoticeCarriesEverythingItClaims(t *testing.T) {
	b := fromInfo(info(debug.BuildSetting{Key: "vcs.revision", Value: "e243d64cd80e394af5f05919c8ab6d32f9bf79e9"}))
	n := b.Notice()
	for _, want := range []string{"madplayer", "e243d64", Author, "Affero", SourceURL} {
		if !strings.Contains(n, want) {
			t.Errorf("the notice does not carry %q:\n%s", want, n)
		}
	}
}

// The running binary answers too — the toolchain stamps this automatically, and
// a build where it did not would ship a panel with nothing in it.
func TestTheRunningBinaryKnowsItsOwnToolchain(t *testing.T) {
	b := Current()
	if b.Go == "" || b.Platform == "" {
		t.Errorf("Current() = %+v, want at least a toolchain and a platform", b)
	}
	if !strings.Contains(b.Platform, "/") {
		t.Errorf("Platform = %q, want os/arch", b.Platform)
	}
}
