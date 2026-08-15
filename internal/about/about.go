// Package about is what this build IS: which commit, which toolchain, which
// madshare, and where its source can be had.
//
// It exists because of the licence rather than for vanity. madplayer links
// madshare, which is AGPL-3.0-or-later, so the whole program is — and the AGPL
// asks two things of a build that is handed to somebody. Section 6 wants an
// offer of the Corresponding Source when a binary is conveyed. Section 13 goes
// further and is the one that actually bites here: anybody "interacting with it
// remotely through a computer network" must be offered the source too, and this
// program is a madnetwork node that SERVES blobs to other people's devices. A
// player that seeds is a network service in exactly the sense that section was
// written for.
//
// An offer is only worth something if it names WHICH source, so the commit is
// not decoration: source that does not correspond to the binary is not
// Corresponding Source. Everything here is read from what the Go toolchain
// already stamps into the binary (debug.ReadBuildInfo) — no ldflags, no build
// script, and nothing that can be forgotten in one of the three ways this is
// built.
package about

import (
	"runtime"
	"runtime/debug"
	"strings"
	"time"
)

// Author is whose copyright this is, for the notice the licence requires. The
// full name, matching LICENSE.md — a notice that names somebody differently from
// the licence beside it is a notice somebody has to reconcile.
const Author = "Kian Eugen Seibel"

// License is the licence in the words the AGPL's own "how to apply" section
// suggests: name it, offer the option of a later version, and disclaim warranty.
const License = "GNU Affero General Public License, version 3 or later"

// SourceURL is where this client's own source will live, confirmed by the author
// as the address he will publish it at (2026-08-16).
//
// It is a constant rather than a string in the panel so that publishing is one
// edit, and it is shown either way: an address that does not answer yet is still
// the honest answer to "where is the source", as long as the panel says which of
// the two it is (see Published).
const SourceURL = "https://github.com/KianGreenMoon/madplayer"

// Published reports whether SourceURL actually serves the source yet. Flip it
// with the repository, in the same commit.
//
// While it is false the panel makes the OTHER offer the licence allows — ask the
// author — because pointing somebody at a 404 and calling it compliance would be
// worse than saying plainly that it is not up yet.
const Published = false

// EngineURL is madshare, which is not a dependency this program happens to use
// but the thing it is mostly made of: the library engine, the database, the
// federation, all of it, running in this process. Its source is public, and so
// is that of every third-party library in go.mod, which leaves this client's own
// layer as the only part of the Corresponding Source not yet on a server.
//
// Reachable as of 2026-08-16 (checked, 200). Note that madshare declares its
// module path as daemonlord.ygg/madshare — a Yggdrasil-only name — so this
// address is for reading and cloning rather than for `go get`.
const EngineURL = "https://github.com/KianGreenMoon/madshare"

// Build is what a person needs to identify this binary, and what somebody
// auditing the source needs to match it.
type Build struct {
	// Version is the module version, which for a build from a working tree is a
	// pseudo-version derived from the commit.
	Version string
	// Commit is the full VCS revision, and Short its first 7 characters. Empty
	// when the build was made outside a repository or with -buildvcs=false.
	Commit string
	// Modified reports that the tree had uncommitted changes. Then no published
	// source corresponds to this binary, and the panel must not imply one does.
	Modified bool
	// Time is when the commit was made (not when it was compiled: the toolchain
	// stamps the revision's own time, which is the reproducible one).
	Time time.Time
	// Go and Platform are the toolchain and target.
	Go, Platform string
	// Madshare is the version of the embedded engine — a separate fact from this
	// module's own, and half of what the Corresponding Source has to include.
	Madshare string
}

// Short is the commit abbreviated the way git abbreviates it.
func (b Build) Short() string {
	if len(b.Commit) < 7 {
		return b.Commit
	}
	return b.Commit[:7]
}

// Identified reports whether this build can be matched to a source revision at
// all. A build with no commit, or one made from a dirty tree, cannot.
func (b Build) Identified() bool { return b.Commit != "" && !b.Modified }

// Current reads the running binary's own build information.
func Current() Build {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return Build{Go: runtime.Version(), Platform: platform()}
	}
	return fromInfo(info)
}

// fromInfo is Current with the input handed in, which is the whole seam a test
// needs: the three interesting cases (a clean build, a dirty one, and one with
// no VCS stamp at all) are shapes of BuildInfo and nothing else.
func fromInfo(info *debug.BuildInfo) Build {
	b := Build{
		Version:  info.Main.Version,
		Go:       info.GoVersion,
		Platform: platform(),
	}
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			b.Commit = s.Value
		case "vcs.modified":
			b.Modified = s.Value == "true"
		case "vcs.time":
			if t, err := time.Parse(time.RFC3339, s.Value); err == nil {
				b.Time = t
			}
		case "GOOS", "GOARCH":
			// Cross-compiled builds report the TARGET here, while runtime's
			// constants report it too — they agree, and the settings are the
			// ones present in a binary somebody inspects with `go version -m`.
		}
	}
	for _, d := range info.Deps {
		if d.Path != madsharePath {
			continue
		}
		b.Madshare = d.Version
		// A replaced module reports the replacement's version, and a local
		// directory has none — "(devel)" is what that looks like. Say so rather
		// than printing a word that means nothing off this machine.
		if d.Replace != nil {
			b.Madshare = d.Replace.Version
			if b.Madshare == "" || b.Madshare == "(devel)" {
				b.Madshare = d.Version + " (from a local checkout)"
			}
		}
	}
	return b
}

const madsharePath = "daemonlord.ygg/madshare"

func platform() string { return runtime.GOOS + "/" + runtime.GOARCH }

// BuildLine is the one-line identity of this build, for the panel and for
// anybody quoting it in a bug report.
func (b Build) BuildLine() string {
	switch {
	case b.Commit == "":
		return "built from an unidentified tree"
	case b.Modified:
		return "commit " + b.Short() + ", with uncommitted changes"
	}
	line := "commit " + b.Short()
	if !b.Time.IsZero() {
		line += " · " + b.Time.Format("2 January 2006")
	}
	return line
}

// SourceOffer is what the panel says about getting the source, in the words the
// current state of the world justifies.
//
// Three states, three different sentences, because they are three different
// promises. Published source that corresponds to this binary is the real offer;
// published source with a modified binary is an offer that would mislead; and
// no repository yet is the case the licence answers with a written offer
// instead (§6b — ask the author, who must provide it).
func (b Build) SourceOffer() string {
	switch {
	case !Published:
		return "madshare — the engine this program is mostly made of — is public, and so is " +
			"every library it uses. This client's own source is not up yet: until it is, ask " +
			Author + " for it. The licence entitles you to it either way, and the commit " +
			"above is what to ask for."
	case !b.Identified():
		return "This binary was built from a tree that is not published as it stands, so the " +
			"repository below is where the program lives rather than where this exact build " +
			"came from. Whoever built it owes you its source."
	default:
		return "The source for this exact build — commit " + b.Short() + " — is at the address below, " +
			"together with everything needed to build it."
	}
}

// Warranty is the disclaimer the licence asks to be shown, in one line.
const Warranty = "It comes with absolutely no warranty."

// Notice is the whole legal notice as plain text, which is also what a person
// can copy out of the panel.
func (b Build) Notice() string {
	return strings.Join([]string{
		"madplayer — " + b.BuildLine(),
		"Embeds madshare " + b.Madshare,
		"Copyright (C) 2026 " + Author,
		"Free software under the " + License + ". " + Warranty,
		"This client:  " + SourceURL + published(Published),
		"The engine:   " + EngineURL,
	}, "\n")
}

// published marks an address that does not answer yet, so a notice pasted into a
// bug report cannot be read as a promise the world does not keep.
func published(ok bool) string {
	if ok {
		return ""
	}
	return "  (not published yet)"
}
