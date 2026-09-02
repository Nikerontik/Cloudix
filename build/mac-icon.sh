#!/usr/bin/env bash
# Replaces the .icns Wails generates with a complete one.
#
# Wails builds the icon set from build/appicon.png but emits only the @2x
# variants (16@2x, 32@2x, 128@2x, 256@2x, 512@2x). macOS will downscale those
# when it needs a 1x size, but a full set is what a bundle is supposed to carry
# and it removes any doubt about which representation the Finder picked.
#
# Run after `wails build -platform darwin/*`.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SRC="$ROOT/build/appicon.png"
APP="$ROOT/build/bin/cloudix.app"
TARGET="$APP/Contents/Resources/iconfile.icns"

[ -f "$SRC" ] || { echo "нет $SRC"; exit 1; }
[ -d "$APP" ] || { echo "нет $APP — сначала wails build"; exit 1; }

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
SET="$WORK/icon.iconset"
mkdir -p "$SET"

# name:pixels — both the 1x and the @2x representation of every size.
for entry in \
  16x16:16 16x16@2x:32 \
  32x32:32 32x32@2x:64 \
  128x128:128 128x128@2x:256 \
  256x256:256 256x256@2x:512 \
  512x512:512 512x512@2x:1024
do
  name="${entry%%:*}"
  px="${entry##*:}"
  sips -Z "$px" "$SRC" --out "$SET/icon_$name.png" >/dev/null 2>&1
done

iconutil -c icns "$SET" -o "$TARGET"

# Nudge LaunchServices: it caches by bundle path, so a rebuilt .app can keep
# showing the icon it had before.
LSREG=/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister
[ -x "$LSREG" ] && "$LSREG" -f "$APP" || true
touch "$APP"

echo "iconfile.icns: $(ls "$SET" | wc -l | tr -d ' ') представлений, $(stat -f%z "$TARGET") байт"
