#!/usr/bin/env bash
# Install the muse-companion mini status site ON the PenguPool machine.
#
# The site (src/serve.mjs, Node 22+, no dependencies) serves a phone-first
# live status page. It runs under launchd so it survives reboots, binds only
# to this machine's Tailscale address, and reads PenguPool through the
# paired-device API with its own device token: pair it once under
# Settings › Muse app and save the token in ~/.muse-companion/device-token.
# Every page request needs the site key this script makes; the URL it
# prints carries it.
#
#   ./install-host.sh            # port 7790
#   PORT=7791 ./install-host.sh  # another port
#
# Idempotent: re-running refreshes the code and reloads the agent.
set -euo pipefail
SRC="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEST="$HOME/.muse-companion"
PORT="${PORT:-7790}"
case "$PORT" in ""|*[!0-9]*) echo "PORT must be a number, not $PORT" >&2; exit 1;; esac
LABEL="com.muse-companion.serve"
PLIST="$HOME/Library/LaunchAgents/$LABEL.plist"

NODE="$(command -v node || true)"
[ -n "$NODE" ] || { echo "node not found — install Node 22 or newer first" >&2; exit 1; }
MAJOR="$("$NODE" --version | sed 's/^v//; s/\..*//')"
[ "$MAJOR" -ge 22 ] || { echo "node $("$NODE" --version) is too old — need 22+ (global fetch)" >&2; exit 1; }

mkdir -p "$DEST/src" "$DEST/out" "$HOME/Library/LaunchAgents"
chmod 0700 "$DEST"
if [ ! -s "$DEST/device-token" ]; then
  echo "no device token: pair the site under PenguPool Settings › Muse app, then save the token:" >&2
  echo "  (umask 077; pbpaste > $DEST/device-token)   # and re-run this script" >&2
  exit 1
fi
chmod 0600 "$DEST/device-token"
[ -s "$DEST/site-key" ] || (umask 077; "$NODE" -e 'process.stdout.write(require("crypto").randomBytes(18).toString("base64url"))' > "$DEST/site-key")
chmod 0600 "$DEST/site-key"
KEY="$(cat "$DEST/site-key")"
install -m 0644 "$SRC/src/serve.mjs" "$DEST/src/serve.mjs"
cp "$SRC/README.md" "$SRC/SETUP.md" "$DEST/" 2>/dev/null || true

cat > "$PLIST" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>$LABEL</string>
  <key>ProgramArguments</key>
  <array>
    <string>$NODE</string>
    <string>$DEST/src/serve.mjs</string>
  </array>
  <key>EnvironmentVariables</key>
  <dict>
    <key>MC_PORT</key><string>$PORT</string>
    <key>PATH</key><string>/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin</string>
  </dict>
  <key>WorkingDirectory</key><string>$DEST</string>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardOutPath</key><string>$DEST/out/serve.log</string>
  <key>StandardErrorPath</key><string>$DEST/out/serve.log</string>
</dict>
</plist>
EOF

launchctl bootout "gui/$(id -u)" "$PLIST" 2>/dev/null || true
launchctl bootstrap "gui/$(id -u)" "$PLIST" 2>/dev/null || launchctl load -w "$PLIST"

TSIP="$(tailscale ip -4 2>/dev/null | head -1 || true)"
TSNAME="$(tailscale status --json 2>/dev/null | "$NODE" -e 'let d="";process.stdin.on("data",c=>d+=c).on("end",()=>{try{console.log((JSON.parse(d).Self||{}).DNSName||"")}catch{console.log("")}})' | sed 's/\.$//' || true)"
for i in $(seq 1 20); do
  if [ -n "$TSIP" ] && curl -sf "http://$TSIP:$PORT/health" >/dev/null 2>&1; then
    echo "mini site is live (the key is part of the URL; keep it to your own devices):"
    [ -n "$TSNAME" ] && echo "  http://$TSNAME:$PORT/?key=$KEY"
    echo "  http://$TSIP:$PORT/?key=$KEY"
    echo "logs: $DEST/out/serve.log"
    exit 0
  fi
  sleep 0.5
done
echo "installed, but the health check did not answer — see $DEST/out/serve.log" >&2
exit 1
