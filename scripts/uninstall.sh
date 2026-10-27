#!/bin/sh
# Removes PenguPool AI.app and the `pengupool-ai` link: everything `make install`
# or the prebuilt app put on this Mac. The agents' memory, logs, Compass and team records
# in ~/.pengupool-ai stay unless --purge.
#   scripts/uninstall.sh            remove the app and the links, keep the data
#   scripts/uninstall.sh --purge    also delete ~/.pengupool-ai (memory, Compass, logs: gone for good)
set -eu
APP="$HOME/Applications/PenguPool AI.app"
[ -d "$APP" ] || APP="/Applications/PenguPool AI.app"
LINK="$HOME/.local/bin/pengupool-ai"
PENGU_LINK="$HOME/.local/bin/pengu" # left by v0.2–v0.3, which shipped the pengu CLI
DATA="${PENGUPOOL_HOME:-$HOME/.pengupool-ai}/v2"
CLI="$APP/Contents/Resources/bin/pengupool"

# Running sessions are the owner's work: stop them in the app first.
if [ -x "$CLI" ] && "$CLI" ls --json 2>/dev/null | grep -q '"state": "running"'; then
  echo "PenguPool AI still has running sessions:" >&2
  "$CLI" ls >&2 || true
  echo "Stop them in the app, then run this again." >&2
  exit 1
fi

osascript -e 'tell application id "com.nduwork.pengupool-ai" to quit' >/dev/null 2>&1 || true
OLD=$(pgrep -f "PenguPool AI.app/Contents/Resources/bin/pengupool daemon|\.local/bin/pengupool-ai daemon" || true)
[ -n "$OLD" ] && kill $OLD 2>/dev/null || true

[ -d "$APP" ] && rm -rf "$APP" && echo "removed $APP"
[ -L "$LINK" ] && rm "$LINK" && echo "removed $LINK"
# Only our own: a pengu link into a PenguPool app, never someone else's pengu.
case "$(readlink "$PENGU_LINK" 2>/dev/null || true)" in
*PenguPool*) rm "$PENGU_LINK" && echo "removed $PENGU_LINK" ;;
esac
if [ "${1:-}" = "--purge" ]; then
  rm -rf "${PENGUPOOL_HOME:-$HOME/.pengupool-ai}" && echo "removed ${PENGUPOOL_HOME:-$HOME/.pengupool-ai}"
else
  echo "kept ~/.pengupool-ai (agents' memory, Compass, logs); --purge deletes it"
fi
