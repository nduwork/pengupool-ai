#!/bin/sh
# Checks that uninstall.sh --purge deletes only this version's data folder. Runs in a temporary
# HOME with a stand-in app and stand-in hosts, and stubs osascript, so no real app or daemon is touched.
#   scripts/uninstall_test.sh
set -eu
unset PENGUPOOL_HOME # a developer's own setting must never reach the script under test
cd "$(dirname "$0")"
T=$(mktemp -d)
T=$(cd "$T" && pwd -P)
trap '[ -f "$T/hosts" ] && while read -r p; do kill -KILL "$p" 2>/dev/null || true; done <"$T/hosts"; rm -rf "$T"' EXIT
mkdir -p "$T/bin" "$T/home" "$T/app/PenguPool AI.app"
printf '#!/bin/sh\nexit 0\n' >"$T/bin/osascript"
chmod +x "$T/bin/osascript"
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
# Only this store's host is stopped: another store's keeps running. Stand-in hosts hold their lock.
cat >"$T/bin/pengupool" <<'HOST'
#!/bin/sh
mkdir -p "$3"; echo $$ >"$3/daemon.lock"; exec 3<"$3/daemon.lock"
while :; do sleep 1; done
HOST
chmod +x "$T/bin/pengupool"
host() {
  "$T/bin/pengupool" daemon --store "$1" & HOSTPID=$!; echo "$HOSTPID" >>"$T/hosts"
  for _ in $(seq 1 40); do lsof -a -p "$HOSTPID" -Fn 2>/dev/null | grep -q 'daemon.lock$' && return; sleep 0.1; done
  fail "the stand-in host did not take its lock"
}
host "$T/home/one/v2"; ONE=$HOSTPID
host "$T/home/two/v2"; TWO=$HOSTPID
mkdir -p "$T/app/PenguPool AI.app"
(PENGUPOOL_HOME="$T/home/one" run)
for _ in $(seq 1 20); do kill -0 "$ONE" 2>/dev/null || break; sleep 0.1; done
kill -0 "$ONE" 2>/dev/null && fail "this store's host is still running"
kill -0 "$TWO" 2>/dev/null || fail "another store's host was stopped"
grep -q pgrep uninstall.sh && fail "uninstall.sh still finds hosts with pgrep"
echo "uninstall_test: ok"
