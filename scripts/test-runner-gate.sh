#!/bin/bash
# Job-started hook for the test runner: the runner's .env sets ACTIONS_RUNNER_HOOK_JOB_STARTED to a copy of this
# script kept on the Mac, outside any checkout. A non-zero exit fails the job before any of its steps run, so the
# runner serves only test.yml, for a push to main or a pull request from a branch of this repository. A pull request
# from a fork is refused even when its run has been approved. Both the runner's GITHUB_* variables and the event
# payload GitHub sent must say so. Each refusal is appended to $PENGUPOOL_GATE_LOG.
#   scripts/test-runner-gate_test.sh checks it.
set -u
repo=nduwork/pengupool-ai
main=refs/heads/main
log=${PENGUPOOL_GATE_LOG:-$HOME/Library/Logs/pengupool-test-gate.log}

refuse() {
	printf '%s refused: %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$1" >>"$log" 2>/dev/null
	echo "test gate: refused: $1" >&2
	exit 1
}
# want NAME GOT WANT
want() { [ "$2" = "$3" ] || refuse "$(printf '%s is %q, want %s' "$1" "$2" "$3")"; }

event=${GITHUB_EVENT_PATH-}
[ -f "$event" ] || refuse "no event payload at $(printf %q "$event")"
field() { plutil -extract "$1" raw -o - "$event" 2>/dev/null; }

want GITHUB_REPOSITORY "${GITHUB_REPOSITORY-}" "$repo"
want 'event repository.full_name' "$(field repository.full_name)" "$repo"
case ${GITHUB_WORKFLOW_REF-} in
"$repo/.github/workflows/test.yml@"*) ;;
*) refuse "$(printf 'GITHUB_WORKFLOW_REF is %q, want %s' "${GITHUB_WORKFLOW_REF-}" "$repo/.github/workflows/test.yml@...")" ;;
esac
case ${GITHUB_EVENT_NAME-} in
push)
	want GITHUB_REF "${GITHUB_REF-}" "$main"
	want 'event ref' "$(field ref)" "$main"
	;;
pull_request)
	want 'event pull_request.head.repo.full_name' "$(field pull_request.head.repo.full_name)" "$repo"
	;;
*) refuse "$(printf 'GITHUB_EVENT_NAME is %q, want push or pull_request' "${GITHUB_EVENT_NAME-}")" ;;
esac
exit 0
