// In-app update, the desktop half: the host downloads the new disk image into <store>/updates/<tag>
// (internal/pool/update.go); the app writes the script below, starts it detached and quits. The
// app hands its host's sessions over before it quits (the host exits and leaves them running). The
// script waits for the app to exit, copies the new PenguPool AI.app next to the installed one, hands
// over from any host still running, swaps the two and opens the new app, whose host adopts the
// sessions; a failure reopens the old app, whose host adopts them the same way. Plain functions
// only, so node --test runs them.
import { realpathSync } from "node:fs";
import { dirname, join, sep } from "node:path";

/** The running .app bundle, from the main executable's path (…/PenguPool AI.app/Contents/MacOS/PenguPool AI), or null. */
export function bundleOf(execPath: string): string | null {
  const macos = dirname(execPath);
  const bundle = dirname(dirname(macos));
  return macos.endsWith(`${sep}Contents${sep}MacOS`) && bundle.endsWith(".app") ? bundle : null;
}

/** A disk image the host downloaded: an existing .dmg under <store>/updates/, links resolved. */
export function isUpdateImage(store: string, path: string): boolean {
  try {
    const p = realpathSync(path);
    return p.startsWith(join(realpathSync(store), "updates") + sep) && p.endsWith(".dmg");
  } catch {
    return false; // missing
  }
}

/** Quotes a value for sh. */
export function sq(s: string): string {
  return `'${s.replaceAll("'", `'\\''`)}'`;
}

export interface InstallPlan {
  pid: number; // the app's main process
  bundle: string; // the installed PenguPool AI.app
  dmg: string;
  log: string; // <store>/logs/update.log
  dir: string; // a fresh temporary directory for the script and the mount point
}

/** The install script. Every step is logged; a failure before the swap reopens the installed app. */
export function installScript(p: InstallPlan): string {
  return `#!/bin/sh
# PenguPool update, written by the app; it runs once, detached, after the app quits.
PATH=/usr/bin:/bin:/usr/sbin:/sbin
export PATH
PID=${p.pid}
BUNDLE=${sq(p.bundle)}
NEW=${sq(`${p.bundle}.new`)}
OLD=${sq(`${p.bundle}.old`)}
DMG=${sq(p.dmg)}
LOG=${sq(p.log)}
DIR=${sq(p.dir)}
MNT="$DIR/mnt"
HOST="$BUNDLE/Contents/Resources/bin/pengupool daemon"
CLI="$HOME/.local/bin/pengupool-ai daemon"
MOUNTED=

log() { echo "$(date '+%Y-%m-%d %H:%M:%S') $*" >> "$LOG"; }
# The old host's pids, started from the bundle or the CLI link: its command line starts with $HOST
# or $CLI, compared as plain text (a shell that merely mentions the path is not the host).
hostpids() { ps -axo pid=,command= | HOST="$HOST" CLI="$CLI" awk '{ pid = $1; sub(/^ *[0-9]+ +/, "") } index($0, ENVIRON["HOST"]) == 1 || index($0, ENVIRON["CLI"]) == 1 { print pid }'; }
detach() {
  [ -n "$MOUNTED" ] || return 0
  hdiutil detach "$MNT" -quiet >> "$LOG" 2>&1 || hdiutil detach "$MNT" -force -quiet >> "$LOG" 2>&1 || log "could not detach $MNT"
  MOUNTED=
}
fail() {
  log "failed: $*; reopening the installed app"
  detach
  rm -rf "$NEW"
  open "$BUNDLE"
  exit 1
}

mkdir -p "$(dirname "$LOG")" "$MNT"
log "updating $BUNDLE from $DMG"
i=0
while kill -0 "$PID" 2>/dev/null; do
  i=$((i + 1))
  [ "$i" -ge 60 ] && fail "the app did not quit within 30 s"
  sleep 0.5
done
log "the app quit"
hdiutil attach -nobrowse -readonly -mountpoint "$MNT" "$DMG" >> "$LOG" 2>&1 || fail "could not open the disk image"
MOUNTED=1
[ -d "$MNT/PenguPool AI.app" ] || fail "the disk image has no PenguPool AI.app"
rm -rf "$NEW"
[ -e "$NEW" ] && fail "could not remove $NEW"
ditto "$MNT/PenguPool AI.app" "$NEW" >> "$LOG" 2>&1 || fail "could not copy the new app"
find "$NEW" -xattrname com.apple.quarantine -exec xattr -d -s com.apple.quarantine {} \\; 2>/dev/null # macOS xattr has no -r
log "copied the new app"
detach
# The old host would answer the new app. It hands its sessions over: an app run's host on SIGUSR1
# (SIGTERM would end them with the run), a host started by hand on SIGTERM.
PIDS=$(hostpids)
for p in $PIDS; do
  if ps -o command= -p "$p" | grep -q -- "--owner-pid"; then kill -USR1 "$p" 2>/dev/null; else kill "$p" 2>/dev/null; fi
done
[ -n "$PIDS" ] && log "stopping the old host ($PIDS); its sessions keep running"
i=0
while PIDS=$(hostpids) && [ -n "$PIDS" ]; do
  i=$((i + 1))
  # shellcheck disable=SC2086
  if [ "$i" -ge 20 ]; then kill -KILL $PIDS 2>/dev/null; log "the old host was killed"; break; fi
  sleep 0.5
done
rm -rf "$OLD"
[ -e "$OLD" ] && fail "could not remove $OLD"
mv "$BUNDLE" "$OLD" || fail "could not move the installed app aside"
if ! mv "$NEW" "$BUNDLE"; then
  mv "$OLD" "$BUNDLE" || log "could not restore $OLD"
  fail "could not move the new app into place"
fi
rm -rf "$OLD"
[ -e "$OLD" ] && log "could not remove $OLD (the next update removes it)"
log "installed; opening the new app"
open "$BUNDLE" || log "could not open $BUNDLE"
rm -rf "$DIR"
`;
}
