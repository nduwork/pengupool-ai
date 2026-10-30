#!/usr/bin/env bash
set -euo pipefail
source "$HATCH_HOOK_RUNTIME"

out="$(python3 "$HOME/workspace/pengupool-agent/detect.py" 2>/dev/null || true)"
if [ -z "$out" ]; then
  log "detector produced no output; staying silent" '{}'
  silent "detector error" '{}'
  exit 0
fi

decision="$(printf '%s' "$out" | jq -r '.decision // "silent"')"
reason="$(printf '%s' "$out" | jq -r '.reason // "no reason given"')"
payload="$(printf '%s' "$out" | jq -c '.payload // {}')"

log "pengupool poll" "$payload"

if [ "$decision" = "wake" ]; then
  wake "$reason" "$payload"
else
  silent "$reason" "$payload"
fi
