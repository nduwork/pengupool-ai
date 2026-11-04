#!/bin/bash
# Checks test-runner-gate.sh: it lets through test.yml for a push to main or a pull request from a branch of this
# repository, and refuses (and logs) a fork's pull request, any other branch, event or workflow.
#   scripts/test-runner-gate_test.sh
set -u
cd "$(dirname "$0")"
T=$(mktemp -d)
trap 'rm -rf "$T"' EXIT
fail() { echo "test-runner-gate_test: $*" >&2; exit 1; }
R=nduwork/pengupool-ai

# push_event REPO REF / pr_event REPO HEAD_REPO: write a payload (with the nulls a real one has).
# SENDER (default nduworker) is the event's sender.login.
SENDER=nduworker
push_event() {
	printf '{"ref":"%s","base_ref":null,"repository":{"full_name":"%s"},"sender":{"login":"%s"}}\n' "$2" "$1" "$SENDER" >"$T/event.json"
}
pr_event() {
	printf '{"number":1,"repository":{"full_name":"%s"},"sender":{"login":"%s"},"pull_request":{"merged_at":null,"head":{"ref":"x","repo":%s}}}\n' \
		"$1" "$SENDER" "$2" >"$T/event.json"
}

# gate EVENT REF [VAR=value ...]: runs the gate for test.yml in this repository, overridden by the arguments.
gate() {
	ev=$1 ref=$2; shift 2
	env -i PATH=/usr/bin:/bin HOME="$T" PENGUPOOL_GATE_LOG="$T/gate.log" \
		GITHUB_REPOSITORY=$R GITHUB_EVENT_NAME="$ev" GITHUB_REF="$ref" GITHUB_EVENT_PATH="$T/event.json" \
		GITHUB_WORKFLOW_REF="$R/.github/workflows/test.yml@$ref" GITHUB_ACTOR=nduworker GITHUB_TRIGGERING_ACTOR=nduworker \
		"$@" bash ./test-runner-gate.sh 2>/dev/null
}
passes() { : >"$T/gate.log"; gate "$@" || fail "refused $*: $(cat "$T/gate.log")"; }
# refused WHAT EVENT REF [VAR=value ...]: the gate must refuse and log WHAT.
refused() {
	what=$1; shift
	: >"$T/gate.log"
	gate "$@" && fail "let through: $what"
	grep -qF "refused: $what" "$T/gate.log" || fail "refusal of $what not logged: $(cat "$T/gate.log")"
}

push_event $R refs/heads/main
passes push refs/heads/main
refused GITHUB_REF push refs/heads/feature
refused GITHUB_WORKFLOW_REF push refs/heads/main GITHUB_WORKFLOW_REF=$R/.github/workflows/release.yml@refs/heads/main
refused GITHUB_WORKFLOW_REF push refs/heads/main GITHUB_WORKFLOW_REF=someone/pengupool-ai/.github/workflows/test.yml@refs/heads/main
refused GITHUB_WORKFLOW_REF push refs/heads/main GITHUB_WORKFLOW_REF=
refused GITHUB_REPOSITORY push refs/heads/main GITHUB_REPOSITORY=someone/pengupool-ai
refused GITHUB_EVENT_NAME workflow_dispatch refs/heads/main
refused GITHUB_EVENT_NAME pull_request_target refs/heads/main
refused GITHUB_EVENT_NAME '' refs/heads/main
refused 'no event payload' push refs/heads/main GITHUB_EVENT_PATH="$T/missing.json"
push_event $R refs/heads/feature
refused 'event ref' push refs/heads/main
push_event someone/pengupool-ai refs/heads/main
refused 'event repository.full_name' push refs/heads/main

pr_event $R "{\"full_name\":\"$R\"}"
passes pull_request refs/pull/1/merge
pr_event $R '{"full_name":"someone/pengupool-ai"}'
refused 'event pull_request.head.repo.full_name' pull_request refs/pull/1/merge
pr_event $R null # a fork that was deleted
refused 'event pull_request.head.repo.full_name' pull_request refs/pull/1/merge
pr_event $R "{\"full_name\":\"$R\"}"
refused GITHUB_EVENT_NAME pull_request_target refs/pull/1/merge
refused GITHUB_WORKFLOW_REF pull_request refs/pull/1/merge GITHUB_WORKFLOW_REF=$R/.github/workflows/other.yml@refs/pull/1/merge
refused GITHUB_WORKFLOW_REF pull_request refs/pull/1/merge GITHUB_WORKFLOW_REF=$R/.github/workflows/test.yml@refs/pull/2/merge
echo 'not json' >"$T/event.json"
refused 'event repository.full_name' pull_request refs/pull/1/merge

# Only the maintainers' own accounts: not an app or bot with write access, nor someone else's re-run.
pr_event $R "{\"full_name\":\"$R\"}"
passes pull_request refs/pull/1/merge GITHUB_ACTOR=ndu-bioinfo GITHUB_TRIGGERING_ACTOR=nduworker
refused GITHUB_ACTOR pull_request refs/pull/1/merge 'GITHUB_ACTOR=meta-muse-ai[bot]'
refused GITHUB_ACTOR pull_request refs/pull/1/merge 'GITHUB_ACTOR=dependabot[bot]'
refused GITHUB_ACTOR pull_request refs/pull/1/merge GITHUB_ACTOR=nduwork
refused GITHUB_ACTOR pull_request refs/pull/1/merge 'GITHUB_ACTOR=nduworker ndu-bioinfo'
refused GITHUB_ACTOR pull_request refs/pull/1/merge GITHUB_ACTOR=
refused GITHUB_ACTOR pull_request refs/pull/1/merge 'GITHUB_ACTOR=*'
refused GITHUB_TRIGGERING_ACTOR pull_request refs/pull/1/merge GITHUB_TRIGGERING_ACTOR=someone
for bot in 'meta-muse-ai[bot]' 'dependabot[bot]'; do
	SENDER=$bot pr_event $R "{\"full_name\":\"$R\"}"
	refused 'event sender.login' pull_request refs/pull/1/merge
	SENDER=$bot push_event $R refs/heads/main
	refused 'event sender.login' push refs/heads/main
done

# An empty environment is refused.
push_event $R refs/heads/main
: >"$T/gate.log"
env -i PATH=/usr/bin:/bin HOME="$T" PENGUPOOL_GATE_LOG="$T/gate.log" bash ./test-runner-gate.sh 2>/dev/null && fail "let through an empty environment"
grep -qF 'refused: no event payload' "$T/gate.log" || fail "empty environment refusal not logged"
echo "test-runner-gate_test: ok"
