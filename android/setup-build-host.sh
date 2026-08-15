#!/bin/sh
# Turn a bare Debian/Ubuntu x86_64 box into a host that can run build-apk.sh.
#
# Everything here is what build-apk.sh's prerequisite comment asks for, in the
# order that makes each step's failure legible: a JDK, the SDK, the NDK, gogio.
# It is idempotent — a second run reinstalls nothing and just reprints the two
# environment lines at the end.
#
# Why a JDK 17 and not whatever `default-jdk` is: gogio compiles gio's Java
# sources with `javac -source 1.8 -target 1.8` (gogio/androidbuild.go), and
# recent JDKs have been dropping the old -source levels one by one. 17 still
# accepts 8, and d8 is happy with what it emits. Newer is a gamble, not an
# upgrade.
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

JAVA_HOME=/usr/lib/jvm/java-17-openjdk-amd64
if [ ! -x "$JAVA_HOME/bin/javac" ]; then
	echo "==> JDK 17, unzip, curl"
	apt-get update
	DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
		openjdk-17-jdk-headless unzip curl ca-certificates
fi
export JAVA_HOME
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
sdkmanager="$sdk/cmdline-tools/latest/bin/sdkmanager"
if [ ! -x "$sdkmanager" ]; then
	echo "==> Android command-line tools -> $sdk/cmdline-tools/latest"
	tmp=$(mktemp -d)
	trap 'rm -rf "$tmp"' EXIT INT TERM
	curl -fL -o "$tmp/cmdline-tools.zip" \
		"https://dl.google.com/android/repository/commandlinetools-linux-${CMDLINE_BUILD}_latest.zip"
	unzip -q "$tmp/cmdline-tools.zip" -d "$tmp"
	mkdir -p "$sdk/cmdline-tools"
	rm -rf "$sdk/cmdline-tools/latest"
	mv "$tmp/cmdline-tools" "$sdk/cmdline-tools/latest"
	rm -rf "$tmp"
	trap - EXIT INT TERM
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
latest() {
	"$sdkmanager" --list --channel=0 2>/dev/null |
		tr -d ' ' | cut -d'|' -f1 |
		grep -E "^$1\$" | sort -V | tail -1
}

buildtools=$(latest 'build-tools;[0-9.]+')
platform=$(latest 'platforms;android-[0-9]+')
ndk=$(latest 'ndk;[0-9.]+')
for pkg in "$buildtools" "$platform" "$ndk"; do
	if [ -z "$pkg" ]; then
		echo "refusing: sdkmanager listed no build-tools, platform or NDK." >&2
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
