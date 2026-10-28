#!/bin/sh
# Builds PenguPool AI.app — the Electron desktop app with the Go host bundled inside, for
# Apple Silicon (arm64) only — and installs it where it is (/Applications, else ~/Applications),
# plus the `pengupool-ai` (host) command in ~/.local/bin.
#   scripts/build-app.sh               build and install
#   scripts/build-app.sh --no-install  build only (desktop/dist/mac-arm64/PenguPool AI.app)
set -eu
cd "$(dirname "$0")/.."
ROOT=$(pwd)
OUT="$ROOT/build"
mkdir -p "$OUT/host" "$OUT/tmp" desktop/build

# npm ci again whenever the lock file changed since the last install (a pull that adds packages),
# not only when node_modules is missing.
deps() { [ node_modules/.package-lock.json -nt package-lock.json ] || npm ci; }

echo "==> web app (renderer)"
(cd web && deps && npm run build >"$OUT/tmp/web-build.log" 2>&1) || { cat "$OUT/tmp/web-build.log" >&2; echo "web app build failed (log above)" >&2; exit 1; }

# The build id: the commit, plus a digest of uncommitted changes. An SSH host running another
# build is updated when the app switches to it.
BUILD=$(git rev-parse --short HEAD 2>/dev/null || echo dev)
if [ -n "$(git status --porcelain 2>/dev/null)" ]; then
  BUILD="$BUILD-$( (git diff HEAD; git ls-files --others --exclude-standard | xargs cat 2>/dev/null) | shasum | cut -c1-8)"
fi
LDFLAGS="-X main.Build=$BUILD"

echo "==> go host (arm64), build $BUILD"
rm -f "$OUT/host/pengupool" # go build won't overwrite an earlier universal (lipo) binary
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags "$LDFLAGS" -o "$OUT/host/pengupool" ./cmd/pengupool

echo "==> go host for Linux SSH hosts (amd64 + arm64)"
mkdir -p "$OUT/host/linux"
for arch in amd64 arm64; do
  CGO_ENABLED=0 GOOS=linux GOARCH=$arch go build -trimpath -ldflags "$LDFLAGS" -o "$OUT/host/linux/pengupool-$arch" ./cmd/pengupool
done

echo "==> icon"
[ desktop/build/icon.icns -nt assets/app-icon.svg ] || scripts/make-icon.sh

echo "==> desktop app"
cd desktop
deps
node build.mjs
rm -rf dist
npx electron-builder --mac dir --arm64 --publish never >/dev/null
APP="$ROOT/desktop/dist/mac-arm64/PenguPool AI.app"
# Ad-hoc sign the whole bundle: packaging changed it, and Apple Silicon refuses unsigned code.
codesign --force --deep -s - "$APP" >/dev/null 2>&1 || echo "warning: ad-hoc signing failed"
cd "$ROOT"
rm -rf "$OUT/tmp"
echo "built $APP"

[ "${1:-}" = "--no-install" ] && exit 0

echo "==> install"
# Replace the app where it is installed: /Applications (the disk image's place) when it is there,
# else ~/Applications. Two copies would leave the app the owner opens on the old build.
DEST="$HOME/Applications/PenguPool AI.app"
[ -d "/Applications/PenguPool AI.app" ] && DEST="/Applications/PenguPool AI.app"
mkdir -p "$(dirname "$DEST")" "$HOME/.local/bin"
STORE="${PENGUPOOL_HOME:-$HOME/.pengupool-ai}/v2"
CLI="$DEST/Contents/Resources/bin/pengupool"

APP_WAS_OPEN=
pgrep -f "PenguPool AI.app/Contents/MacOS/PenguPool AI" >/dev/null && APP_WAS_OPEN=1
osascript -e 'tell application id "com.nduwork.pengupool-ai" to quit' >/dev/null 2>&1 || true
sleep 1
# The running host keeps serving from the old binary; stop it so the new one takes over. Its
# sessions keep running and are adopted by the new host. The lock file names the host that owns
# the store however it was started; pgrep also finds one started through the CLI link.
OLD=$(pgrep -f "PenguPool AI.app/Contents/Resources/bin/pengupool daemon|\.local/bin/pengupool-ai daemon" || true)
LOCKED=$(cat "$STORE/daemon.lock" 2>/dev/null || true)
if [ -n "$LOCKED" ] && ps -o command= -p "$LOCKED" 2>/dev/null | grep -q "pengupool.* daemon"; then
  OLD="$OLD $LOCKED"
fi
OLD=$(echo $OLD | tr ' ' '\n' | sort -u | tr '\n' ' ')
if [ -n "${OLD% }" ]; then
  echo "stopping host: pid $OLD"
  kill $OLD
  for _ in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20; do
    LEFT=; for p in $OLD; do kill -0 "$p" 2>/dev/null && LEFT="$LEFT $p"; done
    [ -z "$LEFT" ] && break
    sleep 0.5
  done
  if [ -n "$LEFT" ]; then
    echo "the running host (pid$LEFT) did not stop; nothing was installed. Stop it ('kill$LEFT') and run make install again." >&2
    exit 1
  fi
fi
rm -rf "$DEST"
ditto "$APP" "$DEST"
ln -sfn "$CLI" "$HOME/.local/bin/pengupool-ai"
echo "installed $DEST"
echo "cli: $HOME/.local/bin/pengupool-ai (host)"

# Start the new host the way the app does (desktop/src/host.ts userEnv): detached, with the PATH of
# an interactive login shell, so `claude`, `omp` and `git` resolve as in a terminal (~/.zshrc often
# adds ~/.local/bin), and not this terminal's other variables, which may be an agent session's.
mkdir -p "$STORE/logs"
SH="${SHELL:-/bin/zsh}"
BASE="HOME=$HOME USER=$USER LOGNAME=${LOGNAME:-$USER} SHELL=$SH TERM=${TERM:-xterm-256color}"
USERPATH=$(env -i $BASE "$SH" -l -i -c 'printf "__PATH__%s" "$PATH"' </dev/null 2>/dev/null | sed -n 's/.*__PATH__//p')
[ -n "$USERPATH" ] || USERPATH="$PATH"
env -i $BASE PATH="$USERPATH" nohup "$CLI" daemon --store "$STORE" >>"$STORE/logs/daemon.log" 2>&1 </dev/null &
NEW=
for _ in $(seq 1 40); do
  NEW=$(cat "$STORE/daemon.lock" 2>/dev/null || true)
  [ -n "$NEW" ] && ps -o command= -p "$NEW" 2>/dev/null | grep -q "^$CLI daemon" && "$CLI" web --store "$STORE" >/dev/null 2>&1 && break
  NEW=
  sleep 0.5
done
if [ -z "$NEW" ]; then
  echo "the new host did not start; see $STORE/logs/daemon.log" >&2
  exit 1
fi
echo "host: pid $NEW, $("$CLI" version)"
if [ -n "$APP_WAS_OPEN" ]; then
  env -u ELECTRON_RUN_AS_NODE open "$DEST"
fi
