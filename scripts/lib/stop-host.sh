# stop-host.sh: find and stop the PenguPool host that owns one store. POSIX sh; safe under set -eu.
# No dollar-brace, backquote or backslash here: desktop/src/stophost.ts carries this text verbatim.

# store_host STORE: sets HOST_PID to the pid of the host that owns STORE, or to "" when there is none.
# The pid comes from the store's own lock; the process must be a pengupool host and must hold that
# very lock open, so a stale lock whose pid now belongs to another program or another store's host
# is never matched.
store_host() {
  HOST_PID=
  [ -d "$1" ] || return 0
  _sh_dir=$(cd "$1" && pwd -P) || return 0
  _sh_pid=$(head -n 1 "$_sh_dir/daemon.lock" 2>/dev/null | tr -cd '0-9') || return 0
  [ -n "$_sh_pid" ] || return 0
  _sh_cmd=$(ps -ww -o command= -p "$_sh_pid" 2>/dev/null) || return 0
  case "$_sh_cmd" in
  *pengupool*" daemon" | *pengupool*" daemon "*) ;;
  *) return 0 ;;
  esac
  lsof -a -p "$_sh_pid" -Fn 2>/dev/null | grep -qxF "n$_sh_dir/daemon.lock" || return 0
  HOST_PID=$_sh_pid
}

# stop_store_host STORE [TRIES]: SIGTERM, then wait TRIES half-seconds (default 20, 10 s).
# Returns 0 when there was no host or it exited; 1 when it still runs (HOST_PID names it).
stop_store_host() {
  store_host "$1"
  [ -n "$HOST_PID" ] || return 0
  _sh_n=20
  [ "$#" -lt 2 ] || _sh_n=$2
  kill "$HOST_PID" 2>/dev/null || return 0
  _sh_i=0
  while kill -0 "$HOST_PID" 2>/dev/null; do
    _sh_i=$((_sh_i + 1))
    [ "$_sh_i" -lt "$_sh_n" ] || return 1
    sleep 0.5
  done
  return 0
}
