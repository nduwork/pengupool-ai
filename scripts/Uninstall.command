#!/bin/sh
# Double-click this to remove what Install.command installed.
# The Compass data in ~/.pengupool-muse stays.
set -u
rm -rf "$HOME/.local/share/pengupool-muse"
SHIM="$HOME/.local/bin/pengu"
if grep -q "pengupool-muse shim" "$SHIM" 2>/dev/null; then rm -f "$SHIM"; echo "removed $SHIM"; fi
echo "uninstalled (Compass data in ~/.pengupool-muse kept)"
