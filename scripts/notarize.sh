#!/bin/sh
# Notarizes and staples the app, or signs, notarizes and staples the disk image, when the app was signed
# with a Developer ID (make dist has scripts/build-app.sh do that when the keychain has one); an ad-hoc
# build is left as it is. make dist runs it on the app first, so the app in the image carries its own ticket.
#   scripts/notarize.sh <app>
#   scripts/notarize.sh <dmg> <app>
# Notarization signs in with an App Store Connect API key when ASC_KEY_P8 (the .p8 key, as PEM text or base64),
# ASC_KEY_ID and ASC_ISSUER_ID are all set, as the release workflow sets them from its secrets, and otherwise with
# the keychain profile "pengupool" (xcrun notarytool store-credentials pengupool ...). The key goes to notarytool
# as a file only this user can read, deleted on exit, and is never printed.
set -eu
DMG=$1
APP=${2:-$1}
case "${ASC_KEY_P8:+key}${ASC_KEY_ID:+id}${ASC_ISSUER_ID:+issuer}" in
'' | keyidissuer) ;;
*)
  echo "set all of ASC_KEY_P8, ASC_KEY_ID and ASC_ISSUER_ID, or none to use the keychain profile pengupool" >&2
  exit 1 ;;
esac
NAME=$(codesign -dvv "$APP" 2>&1 | sed -n 's/^Authority=\(Developer ID Application: .*\)/\1/p' | head -1)
if [ -z "$NAME" ]; then
  echo "not notarized: the app is not signed with a Developer ID (ad-hoc build)"
  exit 0
fi

# By certificate hash, so two certificates with the same name are not ambiguous.
ID=$(security find-identity -v -p codesigning | grep -F "\"$NAME\"" | awk '{print $2}' | head -1)
[ -n "$ID" ] || { echo "no keychain identity for $NAME" >&2; exit 1; }
T=$(mktemp -d) # only this user can open it
trap 'rm -rf "$T"' EXIT
if [ -n "${ASC_KEY_P8:-}" ]; then
  case $ASC_KEY_P8 in
  -----BEGIN*) printf '%s\n' "$ASC_KEY_P8" ;;
  *) printf '%s' "$ASC_KEY_P8" | base64 -D ;;
  esac | (umask 077 && cat >"$T/key.p8") # printf is built in, so the key is never in a process's arguments
  grep -q '^-----BEGIN PRIVATE KEY-----' "$T/key.p8" || { echo "ASC_KEY_P8 is not a .p8 key (PEM text or base64)" >&2; exit 1; }
  set -- --key "$T/key.p8" --key-id "$ASC_KEY_ID" --issuer "$ASC_ISSUER_ID"
else
  set -- --keychain-profile pengupool
fi
case $DMG in
*.app) # notarytool takes a zip of the app, not the bundle; the ticket is stapled to the app itself
  SUBMIT="$T/app.zip"
  ditto -c -k --keepParent "$DMG" "$SUBMIT" ;;
*)
  echo "==> signing $DMG"
  codesign --force --timestamp -s "$ID" "$DMG"
  SUBMIT=$DMG ;;
esac

echo "==> notarizing $DMG (this takes a few minutes)"
OUT=$(xcrun notarytool submit "$SUBMIT" "$@" --wait --output-format json) || true
SUB=$(printf '%s' "$OUT" | sed -n 's/.*"id" *: *"\([^"]*\)".*/\1/p')
STATUS=$(printf '%s' "$OUT" | sed -n 's/.*"status" *: *"\([^"]*\)".*/\1/p')
if [ "$STATUS" != "Accepted" ]; then
  echo "notarization failed: status '${STATUS:-none}'" >&2
  printf '%s\n' "$OUT" >&2
  [ -n "$SUB" ] && xcrun notarytool log "$SUB" "$@" >&2
  exit 1
fi

echo "==> stapling"
xcrun stapler staple "$DMG"
xcrun stapler validate "$DMG"
case $DMG in
*.app) spctl -a -t exec -vvv "$DMG" ;;
*) spctl -a -t open --context context:primary-signature -vvv "$DMG" ;;
esac
