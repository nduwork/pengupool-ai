#!/bin/sh
# Checks lib/stop-host.sh: it stops only the host that holds this store's lock, never another store's,
# never a reused pid, and never trips set -e. Stand-in hosts only; no real daemon is touched.
#   scripts/stop-host_test.sh
set -eu
cd "$(dirname "$0")"
T=$(mktemp -d)
T=$(cd "$T" && pwd -P)
cleanup() {
  [ -f "$T/hosts" ] && while read -r p; do kill -KILL "$p" 2>/dev/null || true; done <"$T/hosts"
  rm -rf "$T"
}
trap cleanup EXIT
fail() { echo "stop-host_test: $*" >&2; exit 1; }
mkdir -p "$T/bin"
cat >"$T/bin/pengupool" <<'HOST'
#!/bin/sh
# A stand-in host: `pengupool daemon --store DIR` holds DIR/daemon.lock open with its pid in it.
mkdir -p "$3"; echo $$ >"$3/daemon.lock"; exec 3<"$3/daemon.lock"
[ "${4:-}" = stubborn ] && trap '' TERM
while :; do sleep 1; done
HOST
chmod +x "$T/bin/pengupool"
host() {
  "$T/bin/pengupool" daemon --store "$@" &
  HOSTPID=$!
  echo "$HOSTPID" >>"$T/hosts"
  for _ in $(seq 1 40); do lsof -a -p "$HOSTPID" -Fn 2>/dev/null | grep -q 'daemon.lock$' && return; sleep 0.1; done
  fail "the stand-in host did not take its lock"
}
alive() { kill -0 "$1" 2>/dev/null; }
gone() { for _ in $(seq 1 20); do alive "$1" || return 0; sleep 0.1; done; return 1; }
. ./lib/stop-host.sh

# 1. Two stores, one path a prefix of the other, with a space: only the named one stops.
host "$T/Store A"; A=$HOSTPID
host "$T/Store A Other"; B=$HOSTPID
stop_store_host "$T/Store A" || fail "case 1: returned 1"
[ "$HOST_PID" = "$A" ] || fail "case 1: HOST_PID '$HOST_PID', want $A"
gone "$A" || fail "case 1: the host of Store A is still running"
alive "$B" || fail "case 1: the host of the other store was stopped"

# 2. A stale lock naming another store's host: nothing is signalled.
echo "$B" >"$T/Store A/daemon.lock"
stop_store_host "$T/Store A" || fail "case 2: returned 1"
[ -z "$HOST_PID" ] || fail "case 2: HOST_PID '$HOST_PID'"
alive "$B" || fail "case 2: another store's host was stopped through a stale lock"

# 3. No store, no lock, an empty lock, a lock naming an ended process.
mkdir -p "$T/nolock" "$T/empty" "$T/dead"
: >"$T/empty/daemon.lock"
sh -c 'exit 0' & wait $!; echo $! >"$T/dead/daemon.lock"
for s in "$T/missing" "$T/nolock" "$T/empty" "$T/dead"; do
  stop_store_host "$s" || fail "case 3 ($s): returned 1"
  [ -z "$HOST_PID" ] || fail "case 3 ($s): HOST_PID '$HOST_PID'"
done

# 4. A process that holds the lock open but is not a host: not signalled.
mkdir -p "$T/other"; : >"$T/other/daemon.lock"
sh -c 'exec 3<"$1"; sleep 60' x "$T/other/daemon.lock" & O=$!
echo "$O" >>"$T/hosts"; echo "$O" >"$T/other/daemon.lock"; sleep 0.3
stop_store_host "$T/other" || fail "case 4: returned 1"
[ -z "$HOST_PID" ] || fail "case 4: HOST_PID '$HOST_PID'"
alive "$O" || fail "case 4: a process that is not a host was stopped"

# 5. A host that ignores SIGTERM: returns 1 after the given tries, naming it.
host "$T/S" stubborn; S=$HOSTPID
began=$(date +%s)
if stop_store_host "$T/S" 2; then fail "case 5: returned 0 for a stubborn host"; fi
[ "$HOST_PID" = "$S" ] || fail "case 5: HOST_PID '$HOST_PID', want $S"
[ $(($(date +%s) - began)) -le 3 ] || fail "case 5: took too long"
alive "$S" || fail "case 5: the stubborn host is gone"

# 6. A store reached through a symlink.
host "$T/Store C"; C=$HOSTPID
ln -s "$T/Store C" "$T/link"
stop_store_host "$T/link" || fail "case 6: returned 1"
gone "$C" || fail "case 6: the host behind the symlink is still running"

# 7. Reaching here under set -eu means no case tripped -e.
echo "stop-host_test: ok"
