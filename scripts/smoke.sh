#!/bin/sh
# End-to-end acceptance for PenguPool AI v0.1.0: a real CLI over a throwaway store, the owner
# API with its hash/idempotency contract, and the MCP server over stdio.
#   scripts/smoke.sh
# Everything happens under a throwaway PENGUPOOL_AI_HOME. Exits non-zero on the first failure.
set -eu
repo="$(cd "$(dirname "$0")/.." && pwd)"
tmp="$(mktemp -d)"
export PENGUPOOL_AI_HOME="$tmp/home" PENGUPOOL_MUSE_HOME="$tmp/home/no-legacy" PENGUPOOL_OWNER_TOKEN=smoke
log="$tmp/owner.log"

cleanup() { kill $srv 2>/dev/null || true; rm -rf "$tmp"; }
trap cleanup EXIT

fail() { echo "FAIL: $*" >&2; exit 1; }
ok()   { echo "ok - $*"; }

echo "== build"
(cd "$repo" && go build -o "$tmp/pengu" ./cmd/pengu) || fail "go build"
pg="$tmp/pengu"

echo "== cli"
[ "$("$pg" version)" = "0.1.0" ] || fail "version"
[ -z "$("$pg" list)" ] || fail "fresh store lists nothing"
[ "$("$pg" check '{"tool":"Bash"}')" = "no line allows it" ] || fail "check"
[ "$("$pg" forget missing)" = "no such line" ] || fail "forget"
ok "cli answers over an empty store"

echo "== owner api"
# A leaked server from an interrupted run would serve stale code on the fixed
# port; clear it first so the smoke always exercises this build.
if stale=$(lsof -ti tcp:8479 2>/dev/null); then kill $stale 2>/dev/null || true; sleep 1; fi
"$pg" owner-serve --bind 127.0.0.1 --port 8479 >"$log" 2>&1 &
srv=$!
for _ in $(seq 1 50); do curl -fsS -H "Authorization: Bearer smoke" http://127.0.0.1:8479/lines >/dev/null 2>&1 && break; sleep 0.2; done
[ "$(curl -s -H "Authorization: Bearer smoke" http://127.0.0.1:8479/lines)" = "[]" ] || fail "lines"
curl -s http://127.0.0.1:8479/lines | grep -q unauthorized || fail "no-auth must 401"
prop=$(curl -s -H "Authorization: Bearer smoke" -H "Content-Type: application/json" \
  -d '{"words":"New Compass line: Smoke often","kind":"new-line","ref":{"text":"Smoke often"}}' \
  http://127.0.0.1:8479/proposals)
pid=$(printf '%s' "$prop" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p')
applied=$("$pg" confirm "$pid")
case "$applied" in "new-line "*) ;; *) fail "confirm: $applied";; esac
dup=$(curl -s -H "Authorization: Bearer smoke" -H "Content-Type: application/json" \
  -d '{"option":"x","payload_hash":"y","idempotency_key":"k"}' \
  http://127.0.0.1:8479/questions/missing/answer)
echo "$dup" | grep -q "no such question" || fail "answer contract: $dup"
kill $srv; wait $srv 2>/dev/null || true; srv=""
ok "propose over HTTP, confirm in CLI, contract errors"

echo "== mcp"
out=$(printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}' | PENGUPOOL_AI_HOME="$tmp/home" "$pg" mcp)
echo "$out" | grep -q "owner's Compass" || fail "mcp initialize: $out"
ok "mcp answers on stdio"

echo "SMOKE PASSED"
