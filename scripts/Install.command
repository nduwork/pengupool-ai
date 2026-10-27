#!/bin/sh
# Double-click this from the PenguPool-Muse disk image (or run it from a terminal).
# Installs the app to ~/.local/share/pengupool-muse and links `pengu` into ~/.local/bin.
set -u
SRC="$(cd "$(dirname "$0")" && pwd)/pengupool-muse"
DEST="$HOME/.local/share/pengupool-muse"
BIN="$HOME/.local/bin"
[ -d "$SRC" ] || { echo "Install.command: payload 'pengupool-muse' not found next to this script" >&2; exit 1; }
command -v python3 >/dev/null || { echo "Install.command: python3 3.9+ is required" >&2; exit 1; }
rm -rf "$DEST"; mkdir -p "$DEST" "$BIN"
ditto "$SRC" "$DEST"
cat > "$BIN/pengu" <<'EOF'
#!/bin/sh
# pengupool-muse shim (removed by Uninstall.command)
PYTHONPATH="$HOME/.local/share/pengupool-muse" exec python3 -m pengu "$@"
EOF
chmod +x "$BIN/pengu"
echo "installed: pengu -> $DEST"
case ":$PATH:" in *":$BIN:"*) ;; *) echo "add to PATH: export PATH=\"$BIN:\$PATH\"";; esac
echo "try: pengu list"
