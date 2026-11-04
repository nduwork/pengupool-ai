#!/bin/bash
# Job-started hook for the test runner: the runner's .env sets ACTIONS_RUNNER_HOOK_JOB_STARTED to a copy of this
# script kept on the Mac, outside any checkout. A non-zero exit fails the job before any of its steps run, so the
# runner serves only test.yml, for a push to main or a pull request from a branch of this repository, started by one
# of the maintainers' own accounts (not an app or bot that can push a branch). A pull request from a fork is refused
# even when its run has been approved. Both the runner's GITHUB_* variables and the event
# payload GitHub sent must say so. Each refusal is appended to $PENGUPOOL_GATE_LOG.
#   scripts/test-runner-gate_test.sh checks it.
set -u
repo=nduwork/pengupool-ai
main=refs/heads/main
maintainers=' nduworker ndu-bioinfo '
log=${PENGUPOOL_GATE_LOG:-$HOME/Library/Logs/pengupool-test-gate.log}

refuse() {
	printf '%s refused: %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$1" >>"$log" 2>/dev/null
	echo "test gate: refused: $1" >&2
	exit 1
}
# want NAME GOT WANT
want() { [ "$2" = "$3" ] || refuse "$(printf '%s is %q, want %s' "$1" "$2" "$3")"; }
# maintainer NAME GOT
maintainer() {
	case $2 in
	'' | *' '*) ;;
	*) case $maintainers in *" $2 "*) return ;; esac ;;
	esac
	refuse "$(printf '%s is %q, want one of:%s' "$1" "$2" "${maintainers% }")"
}

event=${GITHUB_EVENT_PATH-}
[ -f "$event" ] || refuse "no event payload at $(printf %q "$event")"
field() { plutil -extract "$1" raw -o - "$event" 2>/dev/null; }

want GITHUB_REPOSITORY "${GITHUB_REPOSITORY-}" "$repo"
want 'event repository.full_name' "$(field repository.full_name)" "$repo"
want GITHUB_WORKFLOW_REF "${GITHUB_WORKFLOW_REF-}" "$repo/.github/workflows/test.yml@${GITHUB_REF-}"
maintainer GITHUB_ACTOR "${GITHUB_ACTOR-}"
maintainer GITHUB_TRIGGERING_ACTOR "${GITHUB_TRIGGERING_ACTOR-}"
maintainer 'event sender.login' "$(field sender.login)"
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
