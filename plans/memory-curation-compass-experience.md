# Design brief: Compass & Experience curation (memory pile-up)

From the owner's Muse, at the owner's request, after research (Devin, Orca,
Mem0, Letta, Zep/Graphiti, LangMem, Claude Code/Dreams, Cursor/Windsurf) and an
audit of `internal/agentdb/compass.go` and `experience.go` plus the live store.
This is a proposal for the Lead to route and review, not a build order.

## Governing principle (owner, Oct 30, 2026)

Balance stability and robustness — you cannot maximise both, and a stable
system that works takes priority. Subtractive and mechanical fixes first;
added machinery lands one piece at a time, behind review; anything that makes
memory cleverer but less predictable loses to the simpler option.

## Evidence from the live store (Oct 30, 2026)

- 23 Compass lines, ~4 KB. Small today — this is trajectory, fixed cheaply now.
- 7 of 23 lines are permission-approval artifacts: "The owner allows bash for
  `<one specific command...>`", truncated mid-command. Episodes worded as
  principles; they will never match a future situation.
- Only 10 of 23 lines carry a "(when ...)" scope, as free text inside the
  wording — not a field retrieval can filter on.
- A 3-line near-duplicate cluster on sessions surviving quit/update/restore,
  two of which pull against each other (keep running through a quit vs. a
  normal quit closes the daemon and all sessions). Belief averaging cannot
  resolve that; only a supersede can.
- Experience dedup is exact-match only (uid / tombstone / identical body hash);
  near-duplicates accumulate. Entries are title + prose summary, no outcome,
  tags, or scope fields.

## What is already right (keep)

Owner-only signal ingestion; belief replayed as Beta with burst dedup, a daily
evidence cap, and read-time forgetting; thresholds themselves learned from
reviewed outcomes; a separate tighter bar for auto-answering; index + FTS +
on-demand full text for Experience.

## Proposals, staged

**Stage 1 — subtractive, low risk**
1. Classify signals at wording time: permission approvals (allow/deny on a
   specific command) never become principle lines. Reserve line-wording for
   said/correction signals and chose signals that answered a genuine
   preference question.
2. Budget the Compass render: a small always-loaded core plus a hard line/token
   cap, instead of every line above Show.

**Stage 2 — structure**
3. Structured scope + trigger fields on a line (team / agent / repo / work
   kind + one-line trigger), replacing free-text "(when...)"; per-prompt
   retrieval by scope + trigger + belief under the budget.
4. Host-side search-before-write: before a line/effect lands, retrieve the
   nearest existing lines and record the operation — ADD / UPDATE / SUPERSEDE /
   NOOP. SUPERSEDE marks the old line superseded_by, excludes it from render,
   keeps it for audit.

**Stage 3 — Experience and upkeep**
5. Light Experience schema (outcome, tags, scope at capture) and the same
   search-before-write over FTS neighbours for near-duplicates.
6. A consolidation pass that proposes a reviewable diff (dedup, conflict
   resolution, Experience-to-Compass promotions) and never edits the live
   store silently; use-telemetry (what was rendered/opened) feeding demotion;
   a small memory audit surface (counts by class, duplicate clusters, render
   size vs budget).

## Ask of the Lead

React to the staging, route Stage 1 for design review when the current release
work allows, and flag anything here that conflicts with the Compass trust model
— proposals may travel as messages; memory itself must not.
