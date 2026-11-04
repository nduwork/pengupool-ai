#!/bin/sh
# Checks how notarize.sh signs in to notarytool: with the App Store Connect API key in ASC_KEY_P8 (PEM or base64),
# ASC_KEY_ID and ASC_ISSUER_ID when all three are set, with the keychain profile when none is, and not at all when
# only some are. The key reaches notarytool only as a file only this user can read, deleted afterwards, and is never
# printed or put in a command's arguments. codesign, security, xcrun and spctl are stand-ins: nothing is submitted.
#   scripts/notarize_test.sh
set -eu
unset ASC_KEY_P8 ASC_KEY_ID ASC_ISSUER_ID # a developer's own setting must never reach the script under test
cd "$(dirname "$0")"
T=$(mktemp -d)
trap 'rm -rf "$T"' EXIT
fail() { echo "notarize_test: $*" >&2; exit 1; }
mkdir -p "$T/bin" "$T/PenguPool AI.app"
cat >"$T/bin/codesign" <<'STUB'
#!/bin/sh
echo "Authority=Developer ID Application: Test Person (ABCDE12345)" >&2
STUB
cat >"$T/bin/security" <<'STUB'
#!/bin/sh
echo '  1) 0123456789ABCDEF "Developer ID Application: Test Person (ABCDE12345)"'
STUB
# xcrun records notarytool's arguments, and a copy and the mode of the key file it was given.
cat >"$T/bin/xcrun" <<'STUB'
#!/bin/sh
[ "$1" = notarytool ] || exit 0
echo "$*" >>"$ARGS"
while [ $# -gt 0 ]; do
  [ "$1" = --key ] && { echo "$2" >"$ARGS.path"; cp "$2" "$ARGS.key"; stat -f %Lp "$2" >"$ARGS.mode"; }
  shift
done
echo "{\"id\":\"sub-1\",\"status\":\"${STATUS:-Accepted}\"}"
STUB
printf '#!/bin/sh\nexit 0\n' >"$T/bin/spctl"
chmod +x "$T/bin/"*
run() { rm -f "$T/args"*; : >"$T/args"; ARGS="$T/args" PATH="$T/bin:$PATH" sh ./notarize.sh "$T/PenguPool AI.app" >"$T/out" 2>&1; }
KEY='-----BEGIN PRIVATE KEY-----
MIGTAgEAMBMGByqGSM49AgEGCCqGSM49AwEHBHkwdwIBAQQgSECRETSECRETSECRET
-----END PRIVATE KEY-----'
B64=$(printf '%s\n' "$KEY" | base64)

# All three set, as PEM and as base64: the key is a file, mode 600, deleted afterwards, and never printed or on argv.
for k in "$KEY" "$B64"; do
  (ASC_KEY_P8=$k ASC_KEY_ID=KEYID123 ASC_ISSUER_ID=issuer-uuid run) || fail "all set: $(cat "$T/out")" # a subshell: sh 3.2 keeps a prefix assignment after a function call
  grep -q -- "^notarytool submit .* --key $(cat "$T/args.path") --key-id KEYID123 --issuer issuer-uuid --wait" "$T/args" ||
    fail "all set: notarytool got: $(cat "$T/args")"
  printf '%s\n' "$KEY" | cmp -s - "$T/args.key" || fail "all set: the key file does not hold the key"
  [ "$(cat "$T/args.mode")" = 600 ] || fail "the key file's mode is $(cat "$T/args.mode"), want 600"
  [ -e "$(cat "$T/args.path")" ] && fail "the key file was left behind"
  grep -q -- '--keychain-profile' "$T/args" && fail "all set: the keychain profile was used too"
  grep -q SECRET "$T/out" "$T/args" && fail "the key was printed or put on argv"
done

# None set: the keychain profile, as before.
run || fail "none set: $(cat "$T/out")"
grep -q -- '^notarytool submit .* --keychain-profile pengupool --wait' "$T/args" || fail "none set: notarytool got: $(cat "$T/args")"
grep -q -- '--key ' "$T/args" && fail "none set: a key was passed"

# Only some set: refused before anything is submitted.
for some in 'ASC_KEY_P8=$KEY' 'ASC_KEY_ID=KEYID123' 'ASC_ISSUER_ID=issuer-uuid' 'ASC_KEY_ID=KEYID123 ASC_ISSUER_ID=issuer-uuid' \
  'ASC_KEY_P8=$KEY ASC_ISSUER_ID=issuer-uuid'; do
  (eval "export $some"; run) && fail "only $some: not refused"
  grep -q 'set all of ASC_KEY_P8, ASC_KEY_ID and ASC_ISSUER_ID' "$T/out" || fail "only $some: unclear message: $(cat "$T/out")"
  [ -s "$T/args" ] && fail "only $some: notarytool ran"
done

# A value that is not a key: refused, nothing submitted.
(ASC_KEY_P8=not-a-key ASC_KEY_ID=KEYID123 ASC_ISSUER_ID=issuer-uuid run) && fail "a bad key was accepted"
grep -q 'ASC_KEY_P8 is not a .p8 key' "$T/out" || fail "bad key: unclear message: $(cat "$T/out")"
[ -s "$T/args" ] && fail "bad key: notarytool ran"

# A failed submission prints notarytool's log, signed in the same way, and still not the key.
(STATUS=Invalid ASC_KEY_P8=$KEY ASC_KEY_ID=KEYID123 ASC_ISSUER_ID=issuer-uuid run) && fail "an Invalid submission passed"
grep -q -- '^notarytool log sub-1 --key .* --key-id KEYID123 --issuer issuer-uuid$' "$T/args" ||
  fail "the log was not fetched with the same sign-in: $(cat "$T/args")"
grep -q SECRET "$T/out" "$T/args" && fail "a failed submission printed the key"
echo "notarize_test: ok"
