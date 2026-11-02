#!/bin/sh
# Checks that uninstall.sh --purge deletes only this version's data folder. Runs in a temporary
# HOME with a stand-in app, and stubs osascript and pgrep so no real app or daemon is touched.
#   scripts/uninstall_test.sh
set -eu
unset PENGUPOOL_HOME # a developer's own setting must never reach the script under test
cd "$(dirname "$0")"
T=$(mktemp -d)
trap 'rm -rf "$T"' EXIT
mkdir -p "$T/bin" "$T/home" "$T/app/PenguPool AI.app"
printf '#!/bin/sh\nexit 0\n' >"$T/bin/osascript"
printf '#!/bin/sh\nexit 1\n' >"$T/bin/pgrep"
chmod +x "$T/bin/osascript" "$T/bin/pgrep"
run() { HOME="$T/home" PATH="$T/bin:$PATH" PENGUPOOL_APP_DIR="$T/app/PenguPool AI.app" sh ./uninstall.sh "$@" >/dev/null; }
fail() { echo "uninstall_test: $*" >&2; exit 1; }

# PENGUPOOL_HOME holds other files: --purge removes v2 and keeps them.
PH="$T/home/shared"
mkdir -p "$PH/v2/logs" && touch "$PH/v2/state.json" "$PH/keep-me"
(PENGUPOOL_HOME="$PH" run --purge) # a subshell: sh 3.2 keeps a prefix assignment after a function call
[ -e "$PH/v2" ] && fail "v2 was not removed"
[ -e "$PH/keep-me" ] || fail "a file beside v2 in PENGUPOOL_HOME was deleted"
[ -e "$T/app/PenguPool AI.app" ] && fail "the app was not removed"

# The default home: v2 goes, and the then-empty ~/.pengupool-ai with it.
mkdir -p "$T/app/PenguPool AI.app" "$T/home/.pengupool-ai/v2" && touch "$T/home/.pengupool-ai/v2/state.json"
run --purge
[ -e "$T/home/.pengupool-ai" ] && fail "the empty ~/.pengupool-ai was kept"

# Without --purge the data stays.
mkdir -p "$T/app/PenguPool AI.app" "$T/home/.pengupool-ai/v2"
run
[ -d "$T/home/.pengupool-ai/v2" ] || fail "data removed without --purge"
echo "uninstall_test: ok"
