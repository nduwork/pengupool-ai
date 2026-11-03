# PenguPool design: an agent team that grows with you

This is the source of truth for how PenguPool is meant to work and why. Features come and go; these
concepts are what they answer to.

## What PenguPool delivers

Four values set PenguPool apart:

1. **AI-native communication carrying dense information.** Agents talk to each other in dense,
   structured messages made for machines. The owner never relays them and never reads the raw
   traffic; they get the one decision that is theirs. *Others:* human-style agent chatter, or
   everything routed through the human.
2. **Team-first design, not task-first.** Other orchestration systems start from the task: a ticket,
   a spawn, a dissolve. PenguPool starts from the team: named members, standing roles, and
   relationships that exist before the job and after it. Tasks flow through the team; the team is
   the product.
3. **Uncertainty-based memory: the Compass as guide.** Preferences and decisions carry weight from
   evidence, and corrections supersede. The Compass guides the team under uncertainty instead of
   pretending certainty, as honestly as a forecast band that widens rather than faking precision.
   *Others:* a memory file that treats a guess and a hard-won rule as equals.
4. **Helpers carry experience across projects**, each with a narrow focus on its domain. The
   reviewer brings standards built on every past review into each new team; experience compounds in
   the helper, not inside one project's folder. Memory is scoped as section 4 describes:
   helper journals are private and portable, a Lead's memory is kept per team, and the Compass
   is the one shared store.

The pains PenguPool answers:

- the human as the message bus between isolated agents;
- work that grades itself;
- unscoped authority with no provenance;
- experience trapped inside one project.

Single-agent memory is not one of them: memory.md and session resume exist, and that pain is solved.

The ten concepts below are the design that serves these values.

## 1. Peer agents, not subagents

Professional development work is a collaboration of long-lived peer agents, each managing its own
repository. The roles:

- **Lead**: plans the work and dispatches it.
- **Builder**: works in its repository and changes code.
- **PR-review helper**: one dedicated reviewer per repository.
- **Research agent**: works under the Lead.

In PenguPool a team is a tree of sessions: the Lead is its root, a builder works in a repository, and a
Helper comes from the Helper pool. Members talk with `pengupool send`, only to their parent
and children.

## 2. Why a helper reviewer

A spawned sub-reviewer's session closes when it finishes, and its detail becomes noise in the
builder's context. The review helper absorbs that noise instead:

- its own workers report to it;
- it re-checks every blocker with a separate verifier;
- the builder receives one short report: outcome, findings, CI.

Its persisted memory of prior reviews makes reviews consistent across sessions, and the isolation
keeps the builder focused on changing code.

## 3. Experience transfers across projects

A helper carries what it has learned into every team it joins. For example, once a helper
knows how the owner sets up a local GitHub runner (their preferences, where files and directories
go), the owner never teaches it again and nobody redoes the research. That is a real saving in tokens.

Skill repositories carry a `memory.md` plus per-session logs. In PenguPool each helper also keeps
a journal of its finished jobs across teams, and `pengupool experience export` / `import` move that
experience between machines.

## 4. Memory, scoped by role, is the key mechanism

The team's memory is scoped by role at two levels, not kept in one shared store:

- **The Lead** remembers what the team did: its plans, dispatches, decisions and outcomes.
- **Each helper** remembers what it did in the team: its own reviews, research and setups.

The team also learns the owner's preferences and updates them from evidence over
time. It is Bayesian in spirit: a preference is superseded, not deleted, and the owner's corrections
are the richest signal. This is what "an agent team that grows with you" means.

In PenguPool each helper's journal is private to it, one entry per finished job. When a
helper leads a team, its journal also keeps one project entry for that team: the Lead's latest
summary of the work, replaced at each capture. The owner's preferences are the one store every member
reads: the Compass. Only the owner's own words become Compass lines, and each line carries a belief
(a Beta estimate) that evidence moves up or down.

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
Pushes, merges, releases and similar acts still come to the owner, unless one of the owner's
confirmed Compass lines allows that exact act.

Every consequential act traces back: owner → gateway → Lead → member → reviewer.

## 7. Auditability

A finished job yields two things: the product, and the record of the dialogue and logs behind it. How
the work was done can be inspected. Helpers fact-check claims, including the gateway's own account
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
