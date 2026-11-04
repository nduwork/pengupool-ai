#!/bin/bash
# Checks release-runner-gate.sh: it lets through only release.yml started by nduworker on main, and refuses
# (and logs) a job when any runner variable or event field says otherwise.
#   scripts/release-runner-gate_test.sh
set -u
cd "$(dirname "$0")"
T=$(mktemp -d)
trap 'rm -rf "$T"' EXIT
fail() { echo "release-runner-gate_test: $*" >&2; exit 1; }

# event REPO REF WORKFLOW SENDER: writes a workflow_dispatch payload (with the nulls a real one has).
event() {
	printf '{"inputs":null,"ref":"%s","workflow":"%s","repository":{"full_name":"%s","mirror_url":null},"sender":{"login":"%s"}}\n' \
		"$2" "$3" "$1" "$4" >"$T/event.json"
}
good_event() { event nduwork/pengupool-ai refs/heads/main .github/workflows/release.yml nduworker; }

# gate [VAR=value ...]: runs the gate with a passing environment, overridden by the arguments.
gate() {
	env -i PATH=/usr/bin:/bin HOME="$T" PENGUPOOL_GATE_LOG="$T/gate.log" \
		GITHUB_REPOSITORY=nduwork/pengupool-ai GITHUB_EVENT_NAME=workflow_dispatch GITHUB_REF=refs/heads/main \
		GITHUB_WORKFLOW_REF=nduwork/pengupool-ai/.github/workflows/release.yml@refs/heads/main \
		GITHUB_ACTOR=nduworker GITHUB_TRIGGERING_ACTOR=nduworker GITHUB_EVENT_PATH="$T/event.json" \
		"$@" bash ./release-runner-gate.sh 2>/dev/null
}
# refused WHAT [VAR=value ...]: the gate must refuse and log WHAT.
refused() {
	what=$1; shift
	: >"$T/gate.log"
	gate "$@" && fail "let through: $what"
	grep -qF "refused: $what" "$T/gate.log" || fail "refusal of $what not logged: $(cat "$T/gate.log")"
}

good_event
gate || fail "refused the real release: $(cat "$T/gate.log")"

refused GITHUB_REPOSITORY GITHUB_REPOSITORY=someone/pengupool-ai
refused GITHUB_EVENT_NAME GITHUB_EVENT_NAME=pull_request
refused GITHUB_EVENT_NAME GITHUB_EVENT_NAME=pull_request_target
refused GITHUB_REF GITHUB_REF=refs/heads/release-runner
refused GITHUB_REF GITHUB_REF=refs/pull/1/merge
refused GITHUB_WORKFLOW_REF GITHUB_WORKFLOW_REF=nduwork/pengupool-ai/.github/workflows/test.yml@refs/heads/main
refused GITHUB_WORKFLOW_REF GITHUB_WORKFLOW_REF=someone/pengupool-ai/.github/workflows/release.yml@refs/heads/main
refused GITHUB_ACTOR GITHUB_ACTOR=someone
refused GITHUB_TRIGGERING_ACTOR GITHUB_TRIGGERING_ACTOR=someone
refused GITHUB_REPOSITORY GITHUB_REPOSITORY=
refused 'no event payload' GITHUB_EVENT_PATH="$T/missing.json"

event someone/pengupool-ai refs/heads/main .github/workflows/release.yml nduworker
refused 'event repository.full_name'
event nduwork/pengupool-ai refs/heads/other .github/workflows/release.yml nduworker
refused 'event ref'
event nduwork/pengupool-ai refs/heads/main .github/workflows/test.yml nduworker
refused 'event workflow'
event nduwork/pengupool-ai refs/heads/main .github/workflows/release.yml someone
refused 'event sender.login'
echo '{}' >"$T/event.json"
refused 'event repository.full_name'
echo 'not json' >"$T/event.json"
refused 'event repository.full_name'

# An unset variable is refused, not read as empty-and-equal.
good_event
: >"$T/gate.log"
env -i PATH=/usr/bin:/bin HOME="$T" PENGUPOOL_GATE_LOG="$T/gate.log" bash ./release-runner-gate.sh 2>/dev/null && fail "let through an empty environment"
grep -qF 'refused: GITHUB_REPOSITORY' "$T/gate.log" || fail "empty environment refusal not logged"
echo "release-runner-gate_test: ok"
