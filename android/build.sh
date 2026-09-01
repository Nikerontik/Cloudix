#!/usr/bin/env bash
# Builds an installable Cloudix APK.
#
# Signed with the Gradle debug key, which is all sideloading needs — no Play
# account, no keystore. Swap in a real signing config before distributing.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
: "${ANDROID_HOME:=/opt/homebrew/share/android-commandlinetools}"
: "${JAVA_HOME:=/opt/homebrew/opt/openjdk@17/libexec/openjdk.jdk/Contents/Home}"
export ANDROID_HOME JAVA_HOME
export ANDROID_NDK_HOME="${ANDROID_NDK_HOME:-$(ls -d "$ANDROID_HOME"/ndk/* 2>/dev/null | head -1)}"
export PATH="$HOME/go/bin:$JAVA_HOME/bin:$PATH"

echo "==> frontend"
(cd "$ROOT/frontend" && npm run build >/dev/null)

echo "==> UI into assets"
rm -rf "$ROOT/android/app/src/main/assets/www"
mkdir -p "$ROOT/android/app/src/main/assets"
cp -R "$ROOT/frontend/dist" "$ROOT/android/app/src/main/assets/www"

echo "==> Go core -> aar"
mkdir -p "$ROOT/android/libs"
(cd "$ROOT/mobile" && gomobile bind -target=android -androidapi 21 \
    -o "$ROOT/android/libs/cloudixmobile.aar" .)

echo "==> apk"
cd "$ROOT/android"
gradle --no-daemon assembleRelease

OUT="$ROOT/build/mobile"
mkdir -p "$OUT"
cp app/build/outputs/apk/release/app-release.apk "$OUT/Cloudix.apk"

echo
echo "готово: $OUT/Cloudix.apk"
ls -lh "$OUT/Cloudix.apk" | awk '{print "   размер:", $5}'
