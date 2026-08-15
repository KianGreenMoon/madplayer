#!/bin/sh
# Turn a bare Debian/Ubuntu x86_64 box into a host that can run build-apk.sh.
#
# Everything here is what build-apk.sh's prerequisite comment asks for, in the
# order that makes each step's failure legible: a JDK, the SDK, the NDK, gogio.
# It is idempotent — a second run reinstalls nothing and just reprints the two
# environment lines at the end.
#
# It is idempotent in the other direction too: JAVA_HOME set in the environment
# wins over anything it would pick.
#
# Usage:  ./setup-build-host.sh [sdk-dir]     (default: /opt/android-sdk)

set -eu

here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
root=$(CDPATH= cd -- "$here/.." && pwd)      # madplayer/
sdk=${1:-/opt/android-sdk}

# The same wall build-apk.sh refuses at, hit here instead so that nobody spends
# four gigabytes of NDK download discovering it. gogio's archNDK() panics on
# linux/arm64 and the NDK ships no host toolchain for it either.
arch=$(uname -m)
if [ "$arch" != "x86_64" ]; then
	echo "refusing: the Android build needs an x86_64 Linux host, this is $arch." >&2
	echo "Nothing installed here would help — see the comment atop build-apk.sh." >&2
	exit 1
fi

command -v go >/dev/null 2>&1 || {
	echo "refusing: no 'go' on PATH. Install the toolchain first; go.mod asks for" >&2
	echo "1.26.4 and will fetch that itself, but it needs a go to ask." >&2
	exit 1
}

# The cmdline-tools zip is ~150 MB, the NDK ~4 GB unpacked. Running out halfway
# through sdkmanager leaves a half-written package it will not notice.
mkdir -p "$sdk"
free=$(df -Pk "$sdk" | awk 'NR==2 {print int($4 / 1048576)}')
if [ "$free" -lt 8 ]; then
	echo "refusing: ${free} GB free on $sdk, the SDK and NDK need about 8." >&2
	exit 1
fi

# The JDK version matters and the package name carrying it does not survive a
# Debian release, so search for a usable one and install the first candidate apt
# admits to having. Pinning openjdk-17 here was wrong: trixie ships no
# openjdk-17 at all, and `apt-get install openjdk-17-jdk-headless` there fails
# the whole transaction rather than falling back to anything.
#
# The order is 21 first. gogio compiles gio's Java with
# `javac -source 1.8 -target 1.8` (gogio/androidbuild.go) and javac has been
# retiring the old -source levels one release at a time, so the version is a
# real constraint — but 21 was checked against that exact invocation and it
# compiles, with the obsolescence warnings and exit 0. 17 stays as the bookworm
# answer. default-jdk-headless is the last resort and may be too new; if javac
# rejects -source 8, install an older JDK and point JAVA_HOME at it, which this
# script honours.
find_jdk() {
	# Trailing * and not -*: Debian names the directory java-21-openjdk-amd64,
	# other distributions leave the architecture off entirely.
	for d in /usr/lib/jvm/java-21-openjdk* /usr/lib/jvm/java-17-openjdk* \
		/usr/lib/jvm/default-java; do
		if [ -x "$d/bin/javac" ]; then
			echo "$d"
			return 0
		fi
	done
	return 1
}

if [ -n "${JAVA_HOME:-}" ] && [ -x "${JAVA_HOME}/bin/javac" ]; then
	echo "==> JDK: honouring JAVA_HOME=$JAVA_HOME"
else
	JAVA_HOME=$(find_jdk || true)
fi
if [ -z "${JAVA_HOME:-}" ]; then
	echo "==> unzip, curl"
	apt-get update
	DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
		unzip curl ca-certificates
	for pkg in openjdk-21-jdk-headless openjdk-17-jdk-headless default-jdk-headless; do
		echo "==> trying $pkg"
		if DEBIAN_FRONTEND=noninteractive apt-get install -y \
			--no-install-recommends "$pkg"; then
			break
		fi
	done
	JAVA_HOME=$(find_jdk || true)
	if [ -z "$JAVA_HOME" ]; then
		echo "refusing: no JDK found under /usr/lib/jvm after installing." >&2
		echo "Install one yourself and re-run with JAVA_HOME set to it." >&2
		exit 1
	fi
fi
export JAVA_HOME
echo "==> JDK $("$JAVA_HOME/bin/javac" -version 2>&1) at $JAVA_HOME"
PATH="$JAVA_HOME/bin:$PATH"
export PATH

# cmdline-tools MUST live at $ANDROID_HOME/cmdline-tools/latest/. sdkmanager
# derives the SDK root by walking two directories up from itself, so unzipping
# it anywhere else makes it install the NDK into a sibling of the SDK and then
# report the NDK as missing. This is the single most common way to get this
# wrong, which is why the layout is spelled out rather than inferred.
#
# The build number below is only the bootstrap: sdkmanager upgrades itself in
# the step after, so pinning it here ages into nothing worse than one extra
# download. Google keeps old builds served indefinitely.
CMDLINE_BUILD=13114758
CMDLINE_NAME=commandlinetools-linux-${CMDLINE_BUILD}_latest.zip
CMDLINE_URL=https://dl.google.com/android/repository/$CMDLINE_NAME
sdkmanager="$sdk/cmdline-tools/latest/bin/sdkmanager"
if [ ! -x "$sdkmanager" ]; then
	# Look for a zip somebody already carried here before reaching for the
	# network — that is the normal path, not the exception. A host behind a
	# filtering proxy answers this URL with a 404 and then stops resolving
	# dl.google.com at all, and 157 MB is a thing you fetch once and copy.
	#
	# $CMDLINE_ZIP names one explicitly; otherwise any
	# commandlinetools-linux-*.zip beside this script, in the working
	# directory or in $HOME is taken. The build number in the name need not
	# match CMDLINE_BUILD: sdkmanager updates itself in the step after, so
	# any recent one bootstraps the same SDK.
	zip=${CMDLINE_ZIP:-}
	if [ -z "$zip" ]; then
		for candidate in "$here"/commandlinetools-linux-*.zip \
			./commandlinetools-linux-*.zip \
			"${HOME:-/root}"/commandlinetools-linux-*.zip; do
			if [ -f "$candidate" ]; then
				zip=$candidate
				break
			fi
		done
	fi

	tmp=$(mktemp -d)
	trap 'rm -rf "$tmp"' EXIT INT TERM

	if [ -n "$zip" ]; then
		echo "==> command-line tools: using $zip"
	else
		echo "==> command-line tools: downloading $CMDLINE_NAME"
		zip=$tmp/$CMDLINE_NAME
		if ! curl -fL --progress-bar -o "$zip" "$CMDLINE_URL"; then
			echo >&2
			echo "refusing: could not fetch the command-line tools." >&2
			echo "If this host has no route to dl.google.com, fetch" >&2
			echo "  $CMDLINE_URL" >&2
			echo "somewhere that does, copy it to ${HOME:-/root}, and run this" >&2
			echo "again — it will be found and used." >&2
			exit 1
		fi
	fi

	# A truncated copy and a proxy's HTML error page are both files, and both
	# unzip into something that fails forty minutes later as "no NDK found".
	# Check for the one entry that has to be in there.
	if ! unzip -l "$zip" | grep -q 'cmdline-tools/bin/sdkmanager'; then
		echo "refusing: $zip is not an Android command-line tools archive" >&2
		echo "(no cmdline-tools/bin/sdkmanager in it). Re-copy it." >&2
		exit 1
	fi

	echo "==> unpacking to $sdk/cmdline-tools/latest"
	unzip -q "$zip" -d "$tmp/unpacked"
	mkdir -p "$sdk/cmdline-tools"
	rm -rf "$sdk/cmdline-tools/latest"
	mv "$tmp/unpacked/cmdline-tools" "$sdk/cmdline-tools/latest"
	rm -rf "$tmp"
	trap - EXIT INT TERM
else
	echo "==> command-line tools: already at $sdk/cmdline-tools/latest"
fi

ANDROID_HOME=$sdk
ANDROID_SDK_ROOT=$sdk
export ANDROID_HOME ANDROID_SDK_ROOT

echo "==> licences"
yes 2>/dev/null | "$sdkmanager" --licenses >/dev/null || true

# Resolve the newest stable version of each package rather than pin one. A pin
# here would be a second thing to remember to bump, and gogio does not care
# which versions it finds — it globs $ANDROID_HOME for the highest build-tools,
# the highest platforms/android-N and the highest ndk/*, and uses those. So
# install exactly one of each and there is nothing for it to choose wrongly.
#
# --channel=0 is stable only; without it the newest "version" is a canary.
#
# The platform pattern is deliberately [0-9]+ and not [0-9.]+, and it is
# load-bearing rather than tidiness. sdkmanager offers platforms;android-37.0,
# android-36.1, android-33-ext4 and android-CANARY on the stable channel, and
# `sort -V` ranks android-37.0 above android-36 — but gogio's latestPlatform()
# parses the part after "android-" with strconv.Atoi and SKIPS anything that is
# not a plain integer (gogio/androidbuild.go). Install one of those and gogio
# fails with "no platforms found" in an SDK that visibly has one.
latest() {
	"$sdkmanager" --list --channel=0 2>/dev/null |
		tr -d ' ' | cut -d'|' -f1 |
		grep -E "^$1\$" | sort -V | tail -1
}

buildtools=$(latest 'build-tools;[0-9.]+')
platform=$(latest 'platforms;android-[0-9]+')

# The NDK is the one package where newest is WRONG, and it is not a matter of
# taste. oto vendors Oboe, whose AAudio compat shim declares the types the
# sysroot does not have yet, guarded by NDK version — and one of those guards
# is off by one:
#
#   // TODO: find the first NDK version containing the following values
#   #if OBOE_USING_NDK && __NDK_MAJOR__ <= 30
#   typedef enum AAudio_FallbackMode : int32_t { ...
#
# (oto/v3@v3.4.0/internal/oboe/oboe_aaudio_AAudioLoader_android.h). NDK 30's
# sysroot DOES define AAudio_FallbackMode, AAudio_StretchMode and
# AAudioPlaybackParameters, so on NDK 30 every one of them is a redefinition
# and the cgo build of internal/oboe fails outright. The TODO says plainly
# that nobody checked which NDK first shipped them; the guess was one too
# high. AAudio_DeviceType right above it uses `< 30` and additionally trips a
# static_assert on 30.
#
# Those symbols arrived with Android B / API 36 (the header defines
# __ANDROID_API_B__ as 36). NDK 29 is the API 36 NDK, so it is suspect for the
# same reason even though the guard nominally covers it. 28 tops out at API 35,
# which predates all of them — hence the default. NDK_SERIES overrides it, for
# the day oto fixes the guard and the newest NDK is the right answer again.
NDK_SERIES=${NDK_SERIES:-28}
ndk=$(latest "ndk;${NDK_SERIES}\\.[0-9.]+")
if [ -z "$ndk" ]; then
	echo "refusing: sdkmanager offers no ndk;${NDK_SERIES}.* — is NDK_SERIES right?" >&2
	echo "Available: $("$sdkmanager" --list --channel=0 2>/dev/null | tr -d ' ' |
		cut -d'|' -f1 | grep -E '^ndk;[0-9.]+$' | cut -d';' -f2 | cut -d. -f1 |
		sort -un | tr '\n' ' ')" >&2
	exit 1
fi
for pkg in "$buildtools" "$platform"; do
	if [ -z "$pkg" ]; then
		echo "refusing: sdkmanager listed no build-tools or platform." >&2
		echo "Usually a proxy eating https://dl.google.com — try '$sdkmanager --list'." >&2
		exit 1
	fi
done

echo "==> $buildtools, $platform, $ndk (the NDK is the slow one)"
"$sdkmanager" --install "platform-tools" "$buildtools" "$platform" "$ndk"

# gogio is the packager itself; the version tracks gioui.org's own cmd module,
# which is released in step with the gioui.org the app builds against.
#
# Test for the binary where go puts it rather than on PATH: this script's PATH
# is not the one the operator will build in, and `command -v gogio` failing
# there would reinstall it on every run.
gobin=$(go env GOBIN)
[ -n "$gobin" ] || gobin=$(go env GOPATH)/bin
if [ ! -x "$gobin/gogio" ]; then
	echo "==> gogio"
	go install gioui.org/cmd/gogio@latest
fi

# The APK build resolves daemonlord.ygg/madshare through a replace directive
# pointing at a sibling checkout (go.mod), so a lone `git clone madplayer` here
# builds nothing. Say so now rather than let it surface as a module error forty
# minutes into an NDK download.
if [ ! -d "$root/../madshare" ]; then
	echo
	echo "WARNING: no madshare checkout beside $root."
	echo "go.mod replaces daemonlord.ygg/madshare with ../madshare and the yggstack"
	echo "fork with ../madshare/third_party/yggstack. Clone it there before building."
fi

cat <<EOF

done. Put these in the shell that runs 'make android':

  export JAVA_HOME=$JAVA_HOME
  export ANDROID_HOME=$sdk
  export PATH=\$JAVA_HOME/bin:$gobin:\$PATH

then:

  cd $root && make android
EOF
