#!/usr/bin/env bash
# Install the Muse companion for PenguPool onto this VM.
#
# Layout installed (the layout the code's paths already assume):
#   ~/workspace/pengupool-agent/  pp.py, detect.py, gadget.py (+ your tokens)
#   ~/workspace/companion/        companion.py, companion symlink, config.json
#   ~/hooks/scripts/              pengupool-status.sh (the poll wrapper)
#
# Idempotent: re-running refreshes the code and keeps config + tokens.
set -euo pipefail
SRC="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

mkdir -p "$HOME/workspace/pengupool-agent" "$HOME/workspace/companion" "$HOME/hooks/scripts"

install -m 0644 "$SRC/src/pp.py"      "$HOME/workspace/pengupool-agent/pp.py"
install -m 0644 "$SRC/src/detect.py"  "$HOME/workspace/pengupool-agent/detect.py"
install -m 0644 "$SRC/src/gadget.py"  "$HOME/workspace/pengupool-agent/gadget.py"
install -m 0644 "$SRC/src/tracker.py" "$HOME/workspace/pengupool-agent/tracker.py"
install -m 0644 "$SRC/src/askcard.py" "$HOME/workspace/pengupool-agent/askcard.py"
install -m 0755 "$SRC/src/companion.py" "$HOME/workspace/companion/companion.py"
ln -sf "$HOME/workspace/companion/companion.py" "$HOME/workspace/companion/companion"
install -m 0755 "$SRC/hook/pengupool-status.sh" "$HOME/hooks/scripts/pengupool-status.sh"

if [ ! -f "$HOME/workspace/companion/config.json" ]; then
  install -m 0644 "$SRC/config.example.json" "$HOME/workspace/companion/config.json"
  echo "seeded ~/workspace/companion/config.json from config.example.json"
fi
if [ ! -f "$HOME/workspace/companion/tracker.json" ]; then
  install -m 0644 "$SRC/tracker.example.json" "$HOME/workspace/companion/tracker.json"
  echo "seeded ~/workspace/companion/tracker.json from tracker.example.json"
fi

echo "installed. Remaining setup (once):"
echo "  1. Put the daemon bootstrap token in ~/workspace/pengupool-agent/token"
echo "     and the paired device token in ~/workspace/pengupool-agent/device-token"
echo "     (both mode 0600; they are never part of this package)."
echo "  2. If the workstation's Tailscale IP is not 100.106.40.12, export"
echo "     PENGUPOOL_HOST=<its tailnet IP> where the hook runs."
echo "  3. Ask Muse to register/enable the 'pengupool-status' hook (60s poll)"
echo "     pointing at ~/hooks/scripts/pengupool-status.sh."
echo "  4. Try it: ~/workspace/companion/companion status"
