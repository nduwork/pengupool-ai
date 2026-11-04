#!/bin/sh
# Checks how notarize.sh signs in to notarytool: with NOTARY_APPLE_ID and NOTARY_PASSWORD (and the team from the
# certificate) when both are set, with the keychain profile when neither is, and not at all when only one is. It
# never prints the password. codesign, security, xcrun and spctl are stand-ins, so nothing is submitted.
#   scripts/notarize_test.sh
set -eu
unset NOTARY_APPLE_ID NOTARY_PASSWORD # a developer's own setting must never reach the script under test
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
cat >"$T/bin/xcrun" <<'STUB'
#!/bin/sh
[ "$1" = notarytool ] && echo "$*" >>"$ARGS"
[ "$2" = submit ] && echo '{"id":"sub-1","status":"Accepted"}'
exit 0
STUB
printf '#!/bin/sh\nexit 0\n' >"$T/bin/spctl"
chmod +x "$T/bin/"*
run() { ARGS="$T/notarytool.args" PATH="$T/bin:$PATH" sh ./notarize.sh "$T/PenguPool AI.app" >"$T/out" 2>&1; }

# Both set: the Apple ID, the password and the certificate's team, and the password is never printed.
: >"$T/notarytool.args"
(NOTARY_APPLE_ID=dev@example.com NOTARY_PASSWORD=abcd-efgh-ijkl-mnop run) || fail "both set: $(cat "$T/out")" # a subshell: sh 3.2 keeps a prefix assignment after a function call
grep -q -- '--apple-id dev@example.com --password abcd-efgh-ijkl-mnop --team-id ABCDE12345' "$T/notarytool.args" ||
  fail "both set: notarytool got: $(cat "$T/notarytool.args")"
grep -q -- '--keychain-profile' "$T/notarytool.args" && fail "both set: the keychain profile was used too"
grep -q abcd-efgh "$T/out" && fail "the password was printed"

# Neither set: the keychain profile, as before.
: >"$T/notarytool.args"
run || fail "neither set: $(cat "$T/out")"
grep -q -- 'submit .* --keychain-profile pengupool --wait' "$T/notarytool.args" || fail "neither set: notarytool got: $(cat "$T/notarytool.args")"
grep -q -- '--apple-id' "$T/notarytool.args" && fail "neither set: an Apple ID was passed"

# Only one set: refused before anything is submitted.
for one in NOTARY_APPLE_ID=dev@example.com NOTARY_PASSWORD=abcd-efgh-ijkl-mnop; do
  : >"$T/notarytool.args"
  (export "$one"; run) && fail "only $one: not refused"
  grep -q 'set both NOTARY_APPLE_ID and NOTARY_PASSWORD' "$T/out" || fail "only $one: unclear message: $(cat "$T/out")"
  [ -s "$T/notarytool.args" ] && fail "only $one: notarytool ran"
  grep -q abcd-efgh "$T/out" && fail "only $one: the password was printed"
done

# A failed submission prints notarytool's log, signed in the same way, and still not the password.
cat >"$T/bin/xcrun" <<'STUB'
#!/bin/sh
[ "$1" = notarytool ] && echo "$*" >>"$ARGS"
[ "$2" = submit ] && echo '{"id":"sub-2","status":"Invalid"}'
exit 0
STUB
: >"$T/notarytool.args"
(NOTARY_APPLE_ID=dev@example.com NOTARY_PASSWORD=abcd-efgh-ijkl-mnop run) && fail "an Invalid submission passed" # a subshell: sh 3.2 keeps a prefix assignment after a function call
grep -q -- '^notarytool log sub-2 --apple-id dev@example.com --password abcd-efgh-ijkl-mnop --team-id ABCDE12345' "$T/notarytool.args" ||
  fail "the log was not fetched with the same sign-in: $(cat "$T/notarytool.args")"
grep -q abcd-efgh "$T/out" && fail "a failed submission printed the password"
echo "notarize_test: ok"
