#!/bin/bash
# Job-started hook for the release runner: the runner's .env sets ACTIONS_RUNNER_HOOK_JOB_STARTED to a copy of
# this script kept on the Mac, outside any checkout. A non-zero exit fails the job before any of its steps run,
# so the runner (and the signing identity and notary profile in its keychain) serves only release.yml, started
# by nduworker with "Run workflow" on main. Both the runner's GITHUB_* variables and the event payload GitHub
# sent must say so. Each refusal is appended to $PENGUPOOL_GATE_LOG.
#   scripts/release-runner-gate_test.sh checks it.
set -u
repo=nduwork/pengupool-ai
user=nduworker
main=refs/heads/main
workflow=.github/workflows/release.yml
log=${PENGUPOOL_GATE_LOG:-$HOME/Library/Logs/pengupool-release-gate.log}

refuse() {
	printf '%s refused: %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$1" >>"$log" 2>/dev/null
	echo "release gate: refused: $1" >&2
	exit 1
}
# want NAME GOT WANT
want() { [ "$2" = "$3" ] || refuse "$(printf '%s is %q, want %s' "$1" "$2" "$3")"; }

want GITHUB_REPOSITORY "${GITHUB_REPOSITORY-}" "$repo"
want GITHUB_EVENT_NAME "${GITHUB_EVENT_NAME-}" workflow_dispatch
want GITHUB_REF "${GITHUB_REF-}" "$main"
want GITHUB_WORKFLOW_REF "${GITHUB_WORKFLOW_REF-}" "$repo/$workflow@$main"
want GITHUB_ACTOR "${GITHUB_ACTOR-}" "$user"
want GITHUB_TRIGGERING_ACTOR "${GITHUB_TRIGGERING_ACTOR-}" "$user"

event=${GITHUB_EVENT_PATH-}
[ -f "$event" ] || refuse "no event payload at $(printf %q "$event")"
field() { plutil -extract "$1" raw -o - "$event" 2>/dev/null; }
want 'event repository.full_name' "$(field repository.full_name)" "$repo"
want 'event ref' "$(field ref)" "$main"
want 'event workflow' "$(field workflow)" "$workflow"
want 'event sender.login' "$(field sender.login)" "$user"
exit 0
