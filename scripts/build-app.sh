#!/bin/sh
# Builds PenguPool AI — the Electron desktop app with the Go host bundled inside, for Apple
# Silicon (arm64) only — and installs it into ~/Applications, plus a `pengu` command in
# ~/.local/bin.
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

# The build id: the commit, plus a digest of uncommitted changes.
BUILD=$(git rev-parse --short HEAD 2>/dev/null || echo dev)
if [ -n "$(git status --porcelain 2>/dev/null)" ]; then
  BUILD="$BUILD-$((git diff HEAD; git ls-files --others --exclude-standard | xargs cat 2>/dev/null) | shasum | cut -c1-8)"
fi
LDFLAGS="-X main.Build=$BUILD"

echo "==> go host (arm64), build $BUILD"
rm -f "$OUT/host/pengu" # go build won't overwrite an earlier universal (lipo) binary
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags "$LDFLAGS" -o "$OUT/host/pengu" ./cmd/pengu

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
DEST="$HOME/Applications/PenguPool AI.app"
mkdir -p "$HOME/Applications" "$HOME/.local/bin"
STORE="${PENGUPOOL_AI_HOME:-$HOME/.pengupool-ai}"
CLI="$DEST/Contents/Resources/bin/pengu"

APP_WAS_OPEN=
pgrep -f "PenguPool AI.app/Contents/MacOS/PenguPool AI" >/dev/null && APP_WAS_OPEN=1
osascript -e 'tell application id "com.nduwork.pengupool-ai" to quit' >/dev/null 2>&1 || true
sleep 1
# The running server keeps serving from the old binary; stop it so the new one takes over.
# (Also match the store's port: an earlier orphan with no parent still answers.)
OLD=$(pgrep -f "PenguPool AI.app/Contents/Resources/bin/pengu owner-serve|\.local/bin/pengu owner-serve" || true)
PORT_OLD=$(lsof -ti tcp:8471 -sTCP:LISTEN 2>/dev/null || true)
OLD="$OLD $PORT_OLD"
if [ -n "${OLD% }" ]; then
  echo "stopping owner API: pid $OLD"
  kill $OLD
  for _ in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20; do
    LEFT=; for p in $OLD; do kill -0 "$p" 2>/dev/null && LEFT="$LEFT $p"; done
    [ -z "$LEFT" ] && break
    sleep 0.5
  done
  if [ -n "$LEFT" ]; then
    echo "the running server (pid$LEFT) did not stop; nothing was installed. Stop it ('kill$LEFT') and run make install again." >&2
    exit 1
  fi
fi
rm -rf "$DEST"
ditto "$APP" "$DEST"
ln -sfn "$CLI" "$HOME/.local/bin/pengu"
echo "installed $DEST"
echo "cli: $HOME/.local/bin/pengu"
codesign --force --deep -s - "$DEST" >/dev/null 2>&1 || echo "warning: ad-hoc signing failed"

mkdir -p "$STORE/logs"
"$CLI" version
if [ -n "$APP_WAS_OPEN" ]; then
  env -u ELECTRON_RUN_AS_NODE open "$DEST"
fi
