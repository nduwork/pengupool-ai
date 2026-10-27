#!/bin/sh
# Renders assets/app-icon.svg into the macOS icon (an .icns). Needs rsvg-convert: sips and
# qlmanage don't render SVG reliably (brew install librsvg).
#   scripts/make-icon.sh [out.icns]    default: desktop/build/icon.icns
set -eu
cd "$(dirname "$0")/.."
OUT="${1:-desktop/build/icon.icns}"
command -v rsvg-convert >/dev/null || { echo "rsvg-convert not found: brew install librsvg" >&2; exit 1; }
TMP=$(mktemp -d)
ICONSET=$TMP/AppIcon.iconset
mkdir -p "$ICONSET" "$(dirname "$OUT")"
# An .icns is a legacy icon: macOS doesn't mask it, so the art goes on Apple's icon grid (an 824 px
# rounded square centred on a 1024 canvas, transparent margin). assets/app-icon.svg stays full-bleed.
cat >"$TMP/icon.svg" <<SVG
<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink" width="1024" height="1024" viewBox="0 0 1024 1024">
<clipPath id="c"><rect x="100" y="100" width="824" height="824" rx="185"/></clipPath>
<image clip-path="url(#c)" x="100" y="100" width="824" height="824" xlink:href="data:image/svg+xml;base64,$(base64 <assets/app-icon.svg | tr -d '\n')"/>
</svg>
SVG
for s in 16 32 128 256 512; do
  rsvg-convert -w $s -h $s "$TMP/icon.svg" -o "$ICONSET/icon_${s}x${s}.png"
  rsvg-convert -w $((s*2)) -h $((s*2)) "$TMP/icon.svg" -o "$ICONSET/icon_${s}x${s}@2x.png"
done
iconutil -c icns "$ICONSET" -o "$OUT"
rm -rf "$TMP"
