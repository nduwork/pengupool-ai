// In-app update, the desktop half: the host downloads the new disk image into <store>/updates/<tag>
// (internal/pool/update.go); the app writes the script below, starts it detached and quits. The
// script waits for the app to exit, copies the new PenguPool AI.app next to the installed one, stops
// this store's old host, swaps the two, opens the new app and removes its own leftovers. Agent sessions keep running: their workers
// outlive the host and the new host adopts them. Plain functions only, so node --test runs them.
import { realpathSync } from "node:fs";
import { dirname, join, sep } from "node:path";

import { STOP_HOST_SH } from "./stophost.ts";

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
  store: string; // <store>: only its host is stopped, and its updates/ emptied after a good install
  path?: string; // the script's PATH; only tests set it
}

/**
 * The install script. Every step is logged, and every way out the shell can trap (success, a
 * failure, HUP, INT or TERM; not SIGKILL or a power cut) ends in finish(): it puts the installed app back if the swap failed, reopens whichever app is there, and
 * removes what the updater left. After a good install nothing of the updater's remains; after a failed
 * one the download stays so the owner can try again.
 */
export function installScript(p: InstallPlan): string {
  return `#!/bin/sh
# PenguPool update, written by the app; it runs once, detached, after the app quits.
PATH=${p.path === undefined ? "/usr/bin:/bin:/usr/sbin:/sbin" : sq(p.path)}
export PATH
PID=${p.pid}
BUNDLE=${sq(p.bundle)}
NEW=${sq(`${p.bundle}.new`)}
OLD=${sq(`${p.bundle}.old`)}
DMG=${sq(p.dmg)}
LOG=${sq(p.log)}
DIR=${sq(p.dir)}
STORE=${sq(p.store)}
MNT="$DIR/mnt"
MOUNTED=
STATUS=1 # 0 once the new app is in place

log() { echo "$(date '+%Y-%m-%d %H:%M:%S') $*" >> "$LOG"; }
${STOP_HOST_SH}
detach() { # MOUNTED is cleared only when the image really is detached
  [ -n "$MOUNTED" ] || return 0
  if hdiutil detach "$MNT" -quiet >> "$LOG" 2>&1 || hdiutil detach "$MNT" -force -quiet >> "$LOG" 2>&1; then MOUNTED=; else log "could not detach $MNT"; fi
}
# After a successful install nothing of the updater's is left. Only these places, by these names:
# never the store's data, never backups/.
tidy() {
  rm -rf "$OLD"; [ -e "$OLD" ] && log "could not remove $OLD"
  if [ -d "$STORE/updates" ] && [ ! -L "$STORE/updates" ]; then
    for d in "$STORE/updates"/* "$STORE/updates"/.[!.]*; do
      [ -e "$d" ] || [ -L "$d" ] || continue
      rm -rf "$d" && log "removed $d"
    done
  fi
  for d in "$(dirname "$DIR")"/pengupool-update-*; do # temp folders earlier failed updates left
    [ -d "$d" ] && [ ! -L "$d" ] && [ "$d" != "$DIR" ] || continue
    hdiutil detach "$d/mnt" -force -quiet > /dev/null 2>&1
    rm -rf "$d" && log "removed $d"
  done
}
finish() { # every way out of the script ends here, once
  trap - EXIT HUP INT TERM
  detach
  rm -rf "$NEW"
  if [ ! -d "$BUNDLE" ] && [ -d "$OLD" ]; then mv "$OLD" "$BUNDLE" || log "could not restore $OLD"; fi
  if [ "$STATUS" = 0 ]; then tidy; log "installed; opening the new app"; else log "reopening the installed app"; fi
  APP="$BUNDLE"; [ -d "$APP" ] || APP="$OLD"
  open "$APP" || { sleep 2; open "$APP"; } || log "could not open $APP"
  if [ -z "$MOUNTED" ]; then rm -rf "$DIR"; else rm -f "$DIR/update.sh"; fi
}
fail() { log "failed: $*"; exit 1; }
trap finish EXIT
trap 'exit 1' HUP INT TERM

mkdir -p "$(dirname "$LOG")" "$MNT"
( umask 077; : >> "$LOG" ); chmod 600 "$LOG" 2> /dev/null
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
# A signed app takes only an update that verifies and is signed by the same team; an ad-hoc build (make install) skips this.
# The team is checked against Apple's Developer ID chain: an ad-hoc signature can claim any TeamIdentifier.
TEAM=$(codesign -dvv "$BUNDLE" 2>&1 | sed -n 's/^TeamIdentifier=//p')
if [ -n "$TEAM" ] && [ "$TEAM" != "not set" ]; then
  codesign --verify --deep --strict -R="anchor apple generic and certificate 1[field.1.2.840.113635.100.6.2.6] exists and certificate leaf[field.1.2.840.113635.100.6.1.13] exists and certificate leaf[subject.OU] = \\"$TEAM\\"" "$MNT/PenguPool AI.app" >> "$LOG" 2>&1 || fail "the new app is not a verified Developer ID app of team $TEAM"
fi
rm -rf "$NEW"
[ -e "$NEW" ] && fail "could not remove $NEW"
ditto "$MNT/PenguPool AI.app" "$NEW" >> "$LOG" 2>&1 || fail "could not copy the new app"
find "$NEW" -xattrname com.apple.quarantine -exec xattr -d -s com.apple.quarantine {} \\; 2>/dev/null # macOS xattr has no -r
log "copied the new app"
detach
# The old host would answer the new app; sessions keep running without it. Only this store's host.
if stop_store_host "$STORE"; then
  [ -n "$HOST_PID" ] && log "stopped the old host ($HOST_PID)"
else
  kill -KILL "$HOST_PID" 2>/dev/null
  log "the old host ($HOST_PID) was killed"
fi
rm -rf "$OLD"
[ -e "$OLD" ] && fail "could not remove $OLD"
mv "$BUNDLE" "$OLD" || fail "could not move the installed app aside"
mv "$NEW" "$BUNDLE" || fail "could not move the new app into place" # finish puts $OLD back
STATUS=0
`;
}
