#!/bin/sh
# Notarizes and staples the app, or signs, notarizes and staples the disk image, when the app was signed
# with a Developer ID (make dist has scripts/build-app.sh do that when the keychain has one); an ad-hoc
# build is left as it is. make dist runs it on the app first, so the app in the image carries its own ticket.
#   scripts/notarize.sh <app>
#   scripts/notarize.sh <dmg> <app>
# Notarization reads the App Store Connect credentials from the keychain profile "pengupool"
# (xcrun notarytool store-credentials pengupool ...); nothing secret is passed here.
set -eu
DMG=$1
APP=${2:-$1}
NAME=$(codesign -dvv "$APP" 2>&1 | sed -n 's/^Authority=\(Developer ID Application: .*\)/\1/p' | head -1)
if [ -z "$NAME" ]; then
  echo "not notarized: the app is not signed with a Developer ID (ad-hoc build)"
  exit 0
fi

# By certificate hash, so two certificates with the same name are not ambiguous.
ID=$(security find-identity -v -p codesigning | grep -F "\"$NAME\"" | awk '{print $2}' | head -1)
[ -n "$ID" ] || { echo "no keychain identity for $NAME" >&2; exit 1; }
case $DMG in
*.app) # notarytool takes a zip of the app, not the bundle; the ticket is stapled to the app itself
  T=$(mktemp -d); trap 'rm -rf "$T"' EXIT
  SUBMIT="$T/app.zip"
  ditto -c -k --keepParent "$DMG" "$SUBMIT" ;;
*)
  echo "==> signing $DMG"
  codesign --force --timestamp -s "$ID" "$DMG"
  SUBMIT=$DMG ;;
esac

echo "==> notarizing $DMG (this takes a few minutes)"
OUT=$(xcrun notarytool submit "$SUBMIT" --keychain-profile pengupool --wait --output-format json) || true
SUB=$(printf '%s' "$OUT" | sed -n 's/.*"id" *: *"\([^"]*\)".*/\1/p')
STATUS=$(printf '%s' "$OUT" | sed -n 's/.*"status" *: *"\([^"]*\)".*/\1/p')
if [ "$STATUS" != "Accepted" ]; then
  echo "notarization failed: status '${STATUS:-none}'" >&2
  printf '%s\n' "$OUT" >&2
  [ -n "$SUB" ] && xcrun notarytool log "$SUB" --keychain-profile pengupool >&2
  exit 1
fi

echo "==> stapling"
xcrun stapler staple "$DMG"
xcrun stapler validate "$DMG"
case $DMG in
*.app) spctl -a -t exec -vvv "$DMG" ;;
*) spctl -a -t open --context context:primary-signature -vvv "$DMG" ;;
esac
