#!/bin/sh
# Install madplayer for the current user: the binary, the icon and the menu entry.
#
# Everything lands under $HOME, so this needs no root and touches nothing the
# package manager owns. There is no uninstall script because there is nothing to
# unpick: the three paths it writes are printed as it goes, and removing them is
# the uninstall.
#
# Why a desktop entry matters beyond the menu: MPRIS clients resolve the
# DesktopEntry property this program publishes ("madplayer") against the
# freedesktop entries, and that is where the desktop's media widget gets the
# NAME and the ICON it draws beside the transport. Without this file it can
# drive the player and has nothing to call it.
set -eu

here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/.." && pwd)

bindir=${BINDIR:-$HOME/.local/bin}
appdir=${APPDIR:-$HOME/.local/share/applications}
icondir=${ICONDIR:-$HOME/.local/share/icons/hicolor/512x512/apps}

command -v go >/dev/null 2>&1 || {
	echo "refusing: go is not on PATH (it lives in ~/.guix-home/profile/bin on the dev machine)" >&2
	exit 1
}

mkdir -p "$bindir" "$appdir" "$icondir"

echo "==> $bindir/madplayer"
(cd "$root" && go build -o "$bindir/madplayer" ./cmd/madplayer)

# The icon is generated rather than committed — same generator the APK build
# uses, so the two cannot drift.
echo "==> $icondir/madplayer.png"
(cd "$root" && go run ./packaging/icon "$icondir/madplayer.png")

echo "==> $appdir/madplayer.desktop"
cp "$here/madplayer.desktop" "$appdir/madplayer.desktop"

# Best-effort: a desktop that caches its menu wants telling. Never fatal — the
# entry is on disk either way, and most environments notice on their own.
if command -v update-desktop-database >/dev/null 2>&1; then
	update-desktop-database "$appdir" 2>/dev/null || true
fi
if command -v gtk-update-icon-cache >/dev/null 2>&1; then
	gtk-update-icon-cache -f -t "$HOME/.local/share/icons/hicolor" 2>/dev/null || true
fi

echo
echo "Installed. Remove those three paths to uninstall."
case ":$PATH:" in
*":$bindir:"*) ;;
*) echo "Note: $bindir is not on your PATH, so \`madplayer\` will only start from the menu." ;;
esac
