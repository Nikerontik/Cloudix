#!/usr/bin/env bash
# Builds an UNSIGNED Cloudix.ipa for sideloading.
#
# Sideloadly / AltStore re-sign the payload with your own Apple ID, so nothing
# here needs a developer account or a provisioning profile. Run from the repo
# root or from ios/.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT="$ROOT/build/mobile"
APP="$OUT/Payload/Cloudix.app"
FW="$ROOT/ios/Frameworks/Cloudixmobile.xcframework/ios-arm64"
MIN_IOS="14.0"

echo "==> frontend"
(cd "$ROOT/frontend" && npm run build >/dev/null)

echo "==> Go core -> xcframework"
export PATH="$HOME/go/bin:$PATH"
rm -rf "$ROOT/ios/Frameworks/Cloudixmobile.xcframework"
(cd "$ROOT/mobile" && gomobile bind -target=ios -o "$ROOT/ios/Frameworks/Cloudixmobile.xcframework" .)

echo "==> app bundle"
rm -rf "$OUT"
mkdir -p "$APP"
cp "$ROOT/ios/Cloudix/Info.plist" "$APP/Info.plist"
cp -R "$ROOT/frontend/dist" "$APP/www"

# The framework is a static archive, so it links straight into the executable:
# no Frameworks/ directory, no @rpath, nothing to embed.
xcrun -sdk iphoneos swiftc \
  -target "arm64-apple-ios$MIN_IOS" \
  -F "$FW" \
  -framework Cloudixmobile \
  -O -whole-module-optimization \
  -o "$APP/Cloudix" \
  "$ROOT"/ios/Cloudix/*.swift

echo "==> ipa"
(cd "$OUT" && zip -qry Cloudix.ipa Payload)
rm -rf "$OUT/Payload"

echo
echo "готово: $OUT/Cloudix.ipa"
echo "подписать и поставить: Sideloadly или AltStore со своим Apple ID"
ls -lh "$OUT/Cloudix.ipa" | awk '{print "   размер:", $5}'
