#!/bin/sh
# Build madplayer as an Android APK.
#
# MUST RUN ON AN x86_64 HOST. Not a preference — gogio resolves the NDK through
# an archNDK() that ends in `panic("unsupported GOARCH: arm64")` on Linux/arm64
# (gioui.org/cmd/gogio/androidbuild.go), the NDK ships no linux-aarch64 host
# toolchain for it to find anyway, and the 16 KB-page emulation wall documented
# for the Capacitor app (docs/architecture/android-app.md) sits behind both. So
# build on x86_64, then `adb install` from wherever you like — adb itself runs
# fine on aarch64.
#
# Prerequisites on the build host:
#   JDK 17+                     javac, keytool
#   Android SDK build-tools     aapt2, d8, zipalign, apksigner
#   Android NDK r23+            $ANDROID_HOME/ndk/<version>
#   go install gioui.org/cmd/gogio@latest
#
# Usage:  ./build-apk.sh [outdir]       (default: ./out)

set -eu

here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
root=$(CDPATH= cd -- "$here/.." && pwd)      # madplayer/
out=${1:-$here/out}

arch=$(uname -m)
os=$(uname -s)
if [ "$os" = "Linux" ] && [ "$arch" != "x86_64" ]; then
	echo "refusing: Android packaging needs an x86_64 Linux host, this is $arch." >&2
	echo "Both gogio and gomobile panic on linux/$arch — see the comment at the top." >&2
	exit 1
fi

: "${ANDROID_HOME:=${ANDROID_SDK_ROOT:-}}"
if [ -z "$ANDROID_HOME" ]; then
	echo "refusing: set ANDROID_HOME (or ANDROID_SDK_ROOT) to your Android SDK." >&2
	exit 1
fi
export ANDROID_HOME ANDROID_SDK_ROOT="$ANDROID_HOME"

mkdir -p "$out"

# The packagers require a launcher icon; generate it rather than commit a blob.
# It is the same generator the desktop entry uses (packaging/).
icon="$here/icon.png"
[ -f "$icon" ] || (cd "$root" && go run ./packaging/icon "$icon")

echo "==> $out/madplayer.apk"
(cd "$root" && gogio \
	-target android \
	-appid ygg.daemonlord.madplayer \
	-icon "$icon" \
	-o "$out/madplayer.apk" \
	./cmd/madplayer)

echo
echo "built:"
ls -la "$out"/*.apk
echo
echo "install on a connected phone:"
echo "  adb install -r $out/madplayer.apk"
echo
echo "It runs standalone: no server and no account. Point it at a folder on the"
echo "phone and it scans, indexes and plays what is there."
