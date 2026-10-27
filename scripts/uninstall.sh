#!/bin/sh
# Removes PenguPool AI.app and the `pengu` link: everything `make install` put on this
# Mac. The Compass, lessons, questions and logs in ~/.pengupool-ai stay unless --purge.
#   scripts/uninstall.sh            remove the app and the link, keep the data
#   scripts/uninstall.sh --purge    also delete ~/.pengupool-ai (Compass, lessons: gone for good)
set -eu
APP="$HOME/Applications/PenguPool AI.app"
[ -d "$APP" ] || APP="/Applications/PenguPool AI.app"
LINK="$HOME/.local/bin/pengu"
DATA="${PENGUPOOL_AI_HOME:-$HOME/.pengupool-ai}"
CLI="$APP/Contents/Resources/bin/pengu"

osascript -e 'tell application id "com.nduwork.pengupool-ai" to quit' >/dev/null 2>&1 || true
OLD=$(pgrep -f "PenguPool AI.app/Contents/Resources/bin/pengu owner-serve|\.local/bin/pengu owner-serve" || true)
[ -n "$OLD" ] && kill $OLD 2>/dev/null || true

[ -d "$APP" ] && rm -rf "$APP" && echo "removed $APP"
[ -L "$LINK" ] && rm "$LINK" && echo "removed $LINK"
if [ "${1:-}" = "--purge" ]; then
  rm -rf "$DATA" && echo "removed $DATA"
else
  echo "kept $DATA (Compass, lessons, logs); --purge deletes it"
fi
