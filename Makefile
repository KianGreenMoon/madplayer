# madplayer — build, run, package.
#
# Everything here wraps something that already worked from a shell; the point of
# the file is that the incantations stop living in people's heads. Two of them
# are load-bearing and easy to get wrong by hand: a release must not be built
# from a dirty tree (the About panel would tell everybody so, and rightly), and
# the Android APK cannot be built on this machine at all.
#
#> madplayer
#>
#>   make build      the binary, here
#>   make run        build and start it
#>   make test       everything, with the race detector
#>   make android    the APK — x86_64 hosts only
#>   make release    a stripped binary and a tarball in dist/
#>   make install    binary + icon + menu entry, under $HOME
#>   make clean      remove what the above wrote
#>
#> `make build --android` is not something make can parse — a leading `--` is a
#> make option rather than a target — so the Android build is `make android`,
#> also spelled `make build-android`.

# Go is not on the default PATH on the developer's machine: it lives in the
# guix profile. Look there rather than fail with "go: command not found", which
# sends people to install a second toolchain.
GO ?= $(shell command -v go 2>/dev/null || echo $(HOME)/.guix-home/profile/bin/go)

# BIN is where `make build` puts the binary. The repo root is deliberate: it is
# what `go build ./...` does anyway, and .gitignore already knows about it.
BIN ?= madplayer
PKG := ./cmd/madplayer
DIST ?= dist

# VERSION names a build for humans: the tag when the commit has one, the tag
# plus a distance when it does not, and `devel` outside a repository. The
# binary's own identity does NOT come from here — the toolchain stamps the
# commit itself, which is what the About section reads (internal/about) — so a
# wrong answer here is a wrong file name and never a wrong claim about source.
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo devel)
PLATFORM := $(shell $(GO) env GOOS)-$(shell $(GO) env GOARCH)
RELEASE_NAME := madplayer-$(VERSION)-$(PLATFORM)

# -s -w drops the symbol table and DWARF, -trimpath the build machine's
# directory names: 43 MB down to 31 MB, measured. The build info SURVIVES all
# three (checked with `go version -m`), which matters more than the size — the
# About section's whole offer of source rests on the commit still being in
# there.
RELEASE_FLAGS := -trimpath -ldflags "-s -w"

.PHONY: help build run test vet fmt android build-android release install clean

help:
	@grep '^#>' $(MAKEFILE_LIST) | sed 's/^#> \{0,1\}//'

build:
	$(GO) build -o $(BIN) $(PKG)

run:
	$(GO) run $(PKG)

# -race because this program is threads around a sound card: a background
# fetcher, a cover loader, an enrolment loop and a UI at sixty frames a second.
# It has caught two real races here, both in code that looked obviously fine.
test:
	$(GO) test -race ./...

vet:
	$(GO) vet ./...

fmt:
	$(GO) fmt ./...

android build-android:
	@./android/build-apk.sh

# release refuses to build from a tree with uncommitted changes, and that is the
# feature rather than an inconvenience. The binary says which commit it came
# from (internal/about) so that its Corresponding Source can be found, and a
# build from a dirty tree correctly reports that no published commit matches it.
# Shipping that to somebody is shipping a licence problem.
#
# DIRTY=1 overrides it for a build you are handing to nobody.
release:
	@if [ -z "$(DIRTY)" ] && [ -n "$$(git status --porcelain 2>/dev/null)" ]; then \
		echo "refusing: the tree has uncommitted changes, so this build could not be" >&2; \
		echo "matched to any published source — and it would say so in About." >&2; \
		echo "commit them, or 'make release DIRTY=1' for a build nobody else gets." >&2; \
		exit 1; \
	fi
	@rm -rf $(DIST)/$(RELEASE_NAME)
	@mkdir -p $(DIST)/$(RELEASE_NAME)
	$(GO) build $(RELEASE_FLAGS) -o $(DIST)/$(RELEASE_NAME)/madplayer $(PKG)
	@cp LICENSE.md README.md $(DIST)/$(RELEASE_NAME)/
	@cp -r packaging $(DIST)/$(RELEASE_NAME)/
	@tar -C $(DIST) -czf $(DIST)/$(RELEASE_NAME).tar.gz $(RELEASE_NAME)
	@cd $(DIST) && sha256sum $(RELEASE_NAME).tar.gz > $(RELEASE_NAME).tar.gz.sha256
	@echo
	@echo "$(DIST)/$(RELEASE_NAME).tar.gz"
	@cat $(DIST)/$(RELEASE_NAME).tar.gz.sha256
	@$(GO) version -m $(DIST)/$(RELEASE_NAME)/madplayer | grep -E 'vcs\.(revision|modified)' | sed 's/^/  /'
	@echo
	@echo "the APK is a separate build on an x86_64 host: make android"

install:
	@./packaging/install-desktop.sh

clean:
	rm -f $(BIN) meshcheck main
	rm -rf $(DIST) android/out android/icon.png
