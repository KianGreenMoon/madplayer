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
#   JDK 17 or 21                javac, jar, keytool — gogio needs -source 1.8
#   Android SDK build-tools     aapt2, d8, zipalign, apksigner
#   Android NDK r23+            $ANDROID_HOME/ndk/<version>
#   gioui.org/cmd@v0.10.0       in the module cache or reachable via GOPROXY —
#                               the script builds its OWN gogio from it (with
#                               the playback service patched into the manifest
#                               template), so a gogio on PATH is not used
#
# On a bare Debian/Ubuntu box, ./setup-build-host.sh installs all four.
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

# The newest installed platform and build-tools, the same way gogio picks them
# — including gogio's rule that a platform with a non-integer suffix
# (android-33-ext4, android-CANARY) does not count.
platform=$(ls -d "$ANDROID_HOME"/platforms/android-* 2>/dev/null | grep -E 'android-[0-9]+$' | sort -V | tail -1)
buildtools=$(ls -d "$ANDROID_HOME"/build-tools/* 2>/dev/null | sort -V | tail -1)
if [ -z "$platform" ] || [ -z "$buildtools" ]; then
	echo "refusing: no Android platform or build-tools under $ANDROID_HOME." >&2
	echo "Run android/setup-build-host.sh first." >&2
	exit 1
fi

# The packagers require a launcher icon; generate it rather than commit a blob.
# It is the same generator the desktop entry uses (packaging/).
icon="$here/icon.png"
[ -f "$icon" ] || (cd "$root" && go run ./packaging/icon "$icon")

# The Java half of internal/mediasession, compiled into a jar IN the package
# directory — that placement is the mechanism, not a convenience: gogio globs
# *.jar from every Go package directory in the import graph and feeds them to
# d8, so a jar lying beside the Go source is how third-party Java gets into
# the APK at all. Compiled here rather than committed, like the icon.
msdir=$root/internal/mediasession
msjar=$msdir/mediasession.jar
if [ ! -f "$msjar" ] || [ "$msdir/PlaybackService.java" -nt "$msjar" ]; then
	echo "==> $msjar"
	classes=$(mktemp -d)
	javac -source 1.8 -target 1.8 -Xlint:-options \
		-bootclasspath "$platform/android.jar" \
		-d "$classes" "$msdir/PlaybackService.java"
	jar cf "$msjar" -C "$classes" .
	rm -rf "$classes"
fi

# gogio, patched. Its AndroidManifest comes out of a fixed template inside its
# own source: permissions can be added by importing gioui.org/app/permission/*
# packages, but a <service> element cannot be expressed at all — and the
# foreground media service that keeps playback alive with the screen off must
# be declared in the manifest. So the build uses its own gogio, built from the
# pinned module source with the service and its permissions inserted into the
# template. The version is pinned because the insertion anchors on exact lines;
# bumping it means re-checking both anchors below.
#
# POST_NOTIFICATIONS is manifest-only here (nothing requests it at runtime):
# on Android 13+ the media controls ride the quick-settings carousel, which a
# MediaSession reaches without notification permission — declaring it merely
# lets the person enable the shade notification in system settings.
GOGIO_VERSION=${GOGIO_VERSION:-v0.10.0}
pgogio=$out/tools/gogio-$GOGIO_VERSION-playback
if [ ! -x "$pgogio" ]; then
	echo "==> gogio $GOGIO_VERSION + the PlaybackService manifest entries"
	(cd "$root" && go mod download "gioui.org/cmd@$GOGIO_VERSION")
	src=$(cd "$root" && go env GOMODCACHE)/gioui.org/cmd@$GOGIO_VERSION
	work=$(mktemp -d)
	cp -R "$src" "$work/cmd"
	chmod -R u+w "$work/cmd"
	ab=$work/cmd/gogio/androidbuild.go

	# Both anchors must match exactly once, or the template moved under the
	# pin and the insertion would land somewhere silent and wrong.
	for anchor in 'android:targetSdkVersion="{{.TargetSDK}}"' '	</activity>'; do
		n=$(grep -cF "$anchor" "$ab") || true
		if [ "$n" != 1 ]; then
			echo "refusing: gogio $GOGIO_VERSION matches anchor '$anchor' $n times, not once." >&2
			echo "The manifest template changed; re-derive the insertion in $0." >&2
			exit 1
		fi
	done

	awk '
		{ print }
		index($0, "android:targetSdkVersion=\"{{.TargetSDK}}\"") {
			print "\t<uses-permission android:name=\"android.permission.FOREGROUND_SERVICE\"/>"
			print "\t<uses-permission android:name=\"android.permission.FOREGROUND_SERVICE_MEDIA_PLAYBACK\"/>"
			print "\t<uses-permission android:name=\"android.permission.POST_NOTIFICATIONS\"/>"
		}
		$0 == "\t\t</activity>" {
			print "\t\t<service android:name=\"ygg.daemonlord.madplayer.PlaybackService\""
			print "\t\t\tandroid:exported=\"false\""
			print "\t\t\tandroid:foregroundServiceType=\"mediaPlayback\"/>"
		}
	' "$ab" >"$ab.patched"
	mv "$ab.patched" "$ab"
	grep -q 'PlaybackService' "$ab" || {
		echo "refusing: the service did not land in the template." >&2
		exit 1
	}

	mkdir -p "$out/tools"
	(cd "$work/cmd" && go build -o "$pgogio" ./gogio)
	rm -rf "$work"
fi

# -checklinkname=0 is required, and only on Android. github.com/wlynxg/anet —
# pulled in by yggdrasil-go, so it arrives through madshare's mesh and is not
# ours to drop — reaches into the standard library with
#
#   //go:linkname zoneCache net.zoneCache
#
# (anet@v0.0.5/interface_android.go:164). It is how the library reads IPv6 zone
# identifiers on a platform where Go's own interface enumeration does not work.
# Go 1.23 made the linker reject pull-linknames into std that std has not
# blessed, so the link fails with "invalid reference to net.zoneCache". v0.0.5
# is anet's newest release, so there is no upgrade to take.
#
# The cost is honest and worth stating: the check is disabled for the WHOLE
# binary, not merely for anet. Only the Android build carries this — the
# desktop build in the Makefile does not, because only interface_android.go
# does it.
# The signing key. gogio signs with ~/.android/debug.keystore when that file
# exists and otherwise generates a THROWAWAY key inside the build's temp dir —
# a brand-new signature every build. Android refuses to update across
# signatures, so every rebuild forced an uninstall, which wipes the app's data
# and made the phone re-enrol as a fresh mesh node each time. Generate the
# keystore ONCE at the path gogio already looks; every later build on this
# host then signs identically and `adb install -r` updates in place.
keystore=$HOME/.android/debug.keystore
if [ ! -f "$keystore" ]; then
	echo "==> $keystore (new signing key: THIS build still needs one last uninstall)"
	mkdir -p "$HOME/.android"
	keytool -genkeypair -keystore "$keystore" -storepass android \
		-keypass android -alias androiddebugkey -keyalg RSA -keysize 2048 \
		-validity 10950 -dname "CN=Android Debug,O=Android,C=US"
fi

echo "==> $out/madplayer.apk"
# -minsdk 21: android.media.session.MediaSession is API 21, and gogio's
# default of 16 would promise five versions the playback service class cannot
# load on.
(cd "$root" && "$pgogio" \
	-target android \
	-appid ygg.daemonlord.madplayer \
	-minsdk 21 \
	-icon "$icon" \
	-ldflags=-checklinkname=0 \
	-o "$out/madplayer.apk" \
	./cmd/madplayer)

# Trust nothing above: the service entry rides on a patched tool and the jar
# on a glob, and either could quietly regress into an APK that installs, runs,
# and dies with the screen — precisely the bug this build exists to fix. Read
# the answer out of the artifact itself.
echo "==> verifying the playback service is in the APK"
manifest=$("$buildtools/aapt2" dump xmltree --file AndroidManifest.xml "$out/madplayer.apk")
for want in PlaybackService FOREGROUND_SERVICE_MEDIA_PLAYBACK; do
	if ! printf '%s\n' "$manifest" | grep -q "$want"; then
		echo "refusing: the APK manifest lacks $want — the patched gogio did not run?" >&2
		exit 1
	fi
done
if ! unzip -p "$out/madplayer.apk" 'classes*.dex' | grep -qa PlaybackService; then
	echo "refusing: no PlaybackService in the dex — was $msjar picked up?" >&2
	exit 1
fi

echo
echo "built:"
ls -la "$out"/*.apk
echo
echo "install on a connected phone:"
echo "  adb install -r $out/madplayer.apk"
echo
echo "It runs standalone: no server and no account. Point it at a folder on the"
echo "phone and it scans, indexes and plays what is there."
