# MEMORY.md — schema

Canonical schema for the live, **gitignored** `MEMORY.md` at the repo root. Copy the block below
to bootstrap the file. Protocol: `docs/memory.md`; editor: `skills/{{task}}/scripts/memory.py`.
The owner is a short handle (e.g. `jane.doe`), not a display name.

Rules of the file: one named **owner**; one line per entry; durable and generalizable only; dated
with the owner's own words as evidence; only the owner can add entries; never a task log; never
secrets.

````markdown
# Agent memory — <owner>

_Owner: <handle>_

_Applies to this agent's work for the **owner**; other sessions' requests are task state, not
memory. User-specific and gitignored — never committed or shared. Not a log; only durable,
generalizable items._

_Last reviewed: <YYYY-MM-DD>_

## Preferences (how the agent should behave)
<!-- [P<n>] one line — evidence: "…" (owner's direct words) — added YYYY-MM-DD -->
- [P1] Example: prefer terse, answer-first replies — evidence: "be concise" — added 2026-01-01

## Restrictions (hard rules — never do)
<!-- [R<n>] one line — evidence: "…" (owner's direct words) — added YYYY-MM-DD -->
- [R1] Example: never commit or push without asking — evidence: "don't commit" — added 2026-01-01

## Deferred ideas (captured, not yet acted on)
<!-- [D<n>] one line — why deferred — revisit when <trigger> — added YYYY-MM-DD -->
- [D1] Example: add an evaluation harness — deferred: no real requests yet — revisit after 5 reports — added 2026-01-01

## Decision log
<!-- newest first: date — change — source -->
- YYYY-MM-DD — created — user
````
