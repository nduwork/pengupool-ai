#!/usr/bin/env bash
# End-to-end acceptance for the PenguPool 2.0 M1 slice. It is the executable form of the exit
# conditions in docs/design/m1-contract.md: a real daemon, a real PTY, a real attach over a pty, and
# a worker that outlives its daemon.
#
#   scripts/smoke.sh [--keep]
#
# Everything happens under a throwaway PENGUPOOL_HOME. Exits non-zero on the first failed check and
# prints what it saw.
set -euo pipefail
# The throwaway daemon's owner is this script, even when it runs inside an agent session.
unset PENGUPOOL_SESSION_ID

keep=0
[[ "${1:-}" == "--keep" ]] && keep=1

repo="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
tmp="$(mktemp -d)"
port="$(python3 - <<'PY'
import socket
s = socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()
PY
)"
export PENGUPOOL_HOME="$tmp/home"
log="$tmp/daemon.log"
pid=""

cleanup() {
  [[ -n "$pid" ]] && kill "$pid" 2>/dev/null || true
  [[ -n "$pid" ]] && wait "$pid" 2>/dev/null || true
  pkill -f "$tmp/bin/pengupool worker" 2>/dev/null || true
  if [[ $keep == 1 ]]; then echo "kept: $tmp"; else rm -rf "$tmp"; fi
}
trap cleanup EXIT

fail() { echo "FAIL: $*" >&2; echo "--- daemon log ---" >&2; cat "$log" >&2 || true; exit 1; }
ok()   { echo "ok - $*"; }

echo "== build"
mkdir -p "$tmp/bin"
(cd "$repo" && go build -o "$tmp/bin/pengupool" ./cmd/pengupool) || fail "go build"
pg="$tmp/bin/pengupool"

echo "== daemon"
"$pg" daemon --http "127.0.0.1:$port" >"$log" 2>&1 &
pid=$!
for _ in $(seq 1 100); do
  [[ -S "$PENGUPOOL_HOME/v2/run/hub.sock" ]] && break
  sleep 0.05
done
[[ -S "$PENGUPOOL_HOME/v2/run/hub.sock" ]] || fail "the daemon never created its unix socket"
ok "daemon is up on 127.0.0.1:$port"

echo "== session with a PTY"
out="$("$pg" new --raw --name smoke --cwd "$tmp" -- /bin/sh -c 'printf "hello-smoke\n"; sleep 300')" || fail "pengupool new"
sid="$(printf '%s' "$out" | cut -f1)"
[[ -n "$sid" ]] || fail "new returned no session id (got: $out)"
"$pg" ls | grep -q smoke || fail "pengupool ls does not show the session"
ok "session $sid is running"

echo "== fresh attach renders a snapshot"
sleep 1
attach_out="$tmp/attach.out"
# `script` gives the attach client a real terminal on stdin/stdout, which it requires. Stdining
# from a pipe that closes after three seconds makes the client detach (stdin EOF) instead of
# waiting for the session to end.
(sleep 2; printf '\034') | script -q /dev/null "$pg" attach smoke >"$attach_out" 2>"$tmp/attach.err" || true
grep -q "hello-smoke" "$attach_out" || { cat "$attach_out" "$tmp/attach.err"; fail "attach did not render the pre-attach screen from a snapshot"; }
pgrep -f "$tmp/bin/pengupool attach" >/dev/null && fail "the attach client did not exit after detaching"
ok "snapshot reached the attached terminal and the client detached cleanly"

echo "== browser app is served from the embedded assets"
curl -fsS "http://127.0.0.1:$port/api/healthz" >/dev/null || fail "/api/healthz"
curl -fsS "http://127.0.0.1:$port/" | grep -qi '<div id="root"' || fail "the app shell is not served at /"
ok "app shell and healthz respond"

echo "== the worker outlives the daemon"
worker_pid="$(pgrep -f "$tmp/bin/pengupool worker" | head -1 || true)"
[[ -n "$worker_pid" ]] || fail "no worker process found"
kill "$pid"; wait "$pid" 2>/dev/null || true; pid=""
sleep 0.5
kill -0 "$worker_pid" 2>/dev/null || fail "the worker died with the daemon (pid $worker_pid)"

# Make the worker log with no daemon on the other end of its stderr. A worker whose stderr is a pipe
# into the daemon dies here with SIGPIPE and takes the agent with it (review finding K5).
wsock=""
for s in "$PENGUPOOL_HOME"/v2/run/*.sock; do
  case "$s" in */hub.sock) ;; *) wsock="$s" ;; esac
done
[[ -n "$wsock" ]] || fail "no worker socket found under run/"
printf '' | nc -U "$wsock" >/dev/null 2>&1 || true
sleep 0.5
kill -0 "$worker_pid" 2>/dev/null || fail "the worker died after logging with no daemon listening (K5)"
ok "the worker survived its daemon exiting and logging"

"$pg" daemon --http "127.0.0.1:$port" >>"$log" 2>&1 &
pid=$!
for _ in $(seq 1 100); do
  [[ -S "$PENGUPOOL_HOME/v2/run/hub.sock" ]] && break
  sleep 0.05
done
sleep 0.3
"$pg" ls | grep -q smoke || fail "the restarted daemon lost the session"
ok "restarted daemon adopted the live worker (pid $worker_pid)"

echo "== kill stops the worker"
"$pg" kill smoke >/dev/null || fail "pengupool kill"
sleep 0.5
kill -0 "$worker_pid" 2>/dev/null && fail "the worker survived kill"
ok "session stopped"

echo "SMOKE PASSED"
