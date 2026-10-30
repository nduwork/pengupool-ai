#!/usr/bin/env bash
# Install the Muse companion for PenguPool onto this VM.
#
# Layout installed (the layout the code's paths already assume):
#   ~/workspace/pengupool-agent/  pp.py, detect.py, gadget.py (+ your device token)
#   ~/workspace/companion/        companion.py, companion symlink, config.json
#   ~/hooks/scripts/              pengupool-status.sh (the poll wrapper)
#
# Idempotent: re-running refreshes the code and keeps config + tokens.
set -euo pipefail
SRC="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

mkdir -m 0700 -p "$HOME/workspace/pengupool-agent" && mkdir -p "$HOME/workspace/companion" "$HOME/hooks/scripts"

install -m 0644 "$SRC/src/pp.py"      "$HOME/workspace/pengupool-agent/pp.py"
install -m 0644 "$SRC/src/detect.py"  "$HOME/workspace/pengupool-agent/detect.py"
install -m 0644 "$SRC/src/gadget.py"  "$HOME/workspace/pengupool-agent/gadget.py"
install -m 0644 "$SRC/src/tracker.py" "$HOME/workspace/pengupool-agent/tracker.py"
install -m 0644 "$SRC/src/askcard.py" "$HOME/workspace/pengupool-agent/askcard.py"
install -m 0755 "$SRC/src/companion.py" "$HOME/workspace/companion/companion.py"
ln -sf "$HOME/workspace/companion/companion.py" "$HOME/workspace/companion/companion"
install -m 0755 "$SRC/hook/pengupool-status.sh" "$HOME/hooks/scripts/pengupool-status.sh"

[ -f "$HOME/workspace/pengupool-agent/device-token" ] && chmod 0600 "$HOME/workspace/pengupool-agent/device-token"

if [ ! -f "$HOME/workspace/companion/config.json" ]; then
  install -m 0644 "$SRC/config.example.json" "$HOME/workspace/companion/config.json"
  echo "seeded ~/workspace/companion/config.json from config.example.json"
fi
if [ ! -f "$HOME/workspace/companion/tracker.json" ]; then
  install -m 0644 "$SRC/tracker.example.json" "$HOME/workspace/companion/tracker.json"
  echo "seeded ~/workspace/companion/tracker.json from tracker.example.json"
fi

echo "installed. Remaining setup (once):"
echo "  1. Pair this VM under PenguPool Settings › Muse app and put the device token in"
echo "     ~/workspace/pengupool-agent/device-token (mode 0600; it is never part of this package)."
echo "     It is the only credential the companion uses."
echo "  2. Export PENGUPOOL_HOST=<the workstation's Tailscale name> where the hook runs."
echo "  3. Ask Muse to register/enable the 'pengupool-status' hook (60s poll)"
echo "     pointing at ~/hooks/scripts/pengupool-status.sh."
echo "  4. Try it: ~/workspace/companion/companion status"
