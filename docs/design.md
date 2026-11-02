# PenguPool design: an agent team that grows with you

This is the source of truth for how PenguPool is meant to work and why. Features come and go; these
concepts are what they answer to.

## 1. Peer agents, not subagents

Professional development work is a collaboration of long-lived peer agents, each managing its own
repository. The roles:

- **Lead**: plans the work and dispatches it.
- **SME / builder**: works in its repository and changes code.
- **PR-review specialist**: one dedicated reviewer per repository.
- **Research agent**: works under the Lead.

In PenguPool a team is a tree of sessions: the Lead is its root, an SME works in a repository, and a
Specialist comes from the Specialist pool. Members talk with `pengupool send`, only to their parent
and children.

## 2. Why a specialist reviewer

A spawned sub-reviewer's session closes when it finishes, and its detail becomes noise in the
builder's context. The review specialist absorbs that noise instead:

- its own workers report to it;
- it re-checks every blocker with a separate verifier;
- the builder receives one short report: outcome, findings, CI.

Its persisted memory of prior reviews makes reviews consistent across sessions, and the isolation
keeps the builder focused on changing code.

## 3. Experience transfers across projects

Specialists share what they have learned. For example, once a specialist knows how the owner sets up
a local GitHub runner (their preferences, where files and directories go), the owner never teaches
it again and nobody redoes the research. That is a real saving in tokens.

Skill repositories carry a `memory.md` plus per-session logs. In PenguPool each specialist also keeps
a journal of its finished jobs across teams, and `pengupool experience export` / `import` move that
experience between machines.

## 4. Shared memory is the key mechanism

The team remembers the owner's preferences and updates them from evidence over time. It is Bayesian
in spirit: a preference is superseded, not deleted, and the owner's corrections are the richest
signal. This is what "an agent team that grows with you" means.

In PenguPool this memory is the Compass. Only the owner's own words become lines, and each line
carries a belief (a Beta estimate) that evidence moves up or down.

## 5. The gateway model

The owner talks to their Muse in natural, messy language. Muse densifies it into organized prompts
to the Lead. The Lead plans and distributes the work. Results come back compressed: one decision for
the owner at a time.

The remote agent's role shifts from solo executor to gateway between the owner and the team the owner
built. That is what makes execution compliant rather than fully autonomous.

## 6. No black box

The owner knows every member and places each one. Each member has its own permissions and
jurisdiction, so no agent goes beyond what it is permitted.

Provenance is structural: an agent's yes is not the owner's yes. In November 2026, for example, an
agent's ask to create the meta-demo repository could be answered only by the owner. In PenguPool
the Lead cannot approve, merge or change direction for the owner. A question is answered in the
owner's name only from the owner: their own prompt, or their recorded decisions and Compass lines.
Pushes, merges, releases and similar acts still come to the owner.

Every consequential act traces back: owner → gateway → Lead → member → reviewer.

## 7. Auditability

A finished job yields two things: the product, and the record of the dialogue and logs behind it. How
the work was done can be inspected. Specialists fact-check claims, including the gateway's own account
of what happened. In PenguPool the team's message log keeps the latest messages between members.

## 8. Notification philosophy

"Needs input" is a signal, relayed as it is emitted. A hybrid triage shapes only push and timing:
a settle window before pushing, with pulling the status as the safety net. A missed push is
acceptable; a false alarm is the cost to avoid.

## 9. Stability over robustness

When the two trade off, a stable system that works takes priority.

## 10. Deliberate non-goal: work tracking

Epics and work-status tracking do not belong in PenguPool. That is the job of third-party management
tools.
